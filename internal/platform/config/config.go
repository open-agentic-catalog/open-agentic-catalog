// Package config loads and validates strict typed runtime configuration.
// Loading combines defaults, an optional JSON file, allowlisted environment
// overrides, and bounded external policies before role-specific validation.
// Secret inputs remain deployment-injected and diagnostic reports redact them.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Duration encodes configuration time spans as Go duration strings, never numbers.
type Duration struct{ time.Duration }

// UnmarshalJSON accepts a duration string and leaves the receiver unchanged when
// JSON decoding or duration parsing fails.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return safeerr.New("duration must be a string")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalJSON emits the canonical duration string used by configuration reports.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Config is the complete strict runtime configuration. LoadFor resolves its
// allowlisted environment overrides and external policy before validation.
type Ratings struct {
	SharingEnabled     bool `json:"sharingEnabled"`
	MinimumSharedCount int  `json:"minimumSharedCount"`
}

type Config struct {
	PublisherDomainVerification PublisherDomainVerification `json:"publisherDomainVerification"`
	Ratings                     Ratings                     `json:"ratings"`
	DemoArchives                DemoArchives                `json:"demoArchives"`
	LogLevel                    string                      `json:"logLevel"`
	LogFormat                   string                      `json:"logFormat"`
	Environment                 string                      `json:"environment"`
	Profile                     string                      `json:"profile"`
	ServiceName                 string                      `json:"serviceName"`
	Validation                  catalog.DiagnosticLimits    `json:"validation"`
	MCP                         MCP                         `json:"mcp"`
	HTTP                        HTTP                        `json:"http"`
	Database                    Database                    `json:"database"`
	Auth                        Auth                        `json:"auth"`
	Jobs                        Jobs                        `json:"jobs"`
	Worker                      Worker                      `json:"worker"`
	Federation                  Federation                  `json:"federation"`
	Discovery                   Discovery                   `json:"discovery"`
	Publication                 Publication                 `json:"publication"`
	Webhooks                    Webhooks                    `json:"webhooks"`
	Tracing                     Tracing                     `json:"tracing"`
	SemanticSearch              Semantic                    `json:"semanticSearch"`
	ListingMedia                ListingMedia                `json:"listingMedia"`
	TrustVerification           TrustVerification           `json:"trustVerification"`
	ManagedMedia                Capability                  `json:"managedMedia"`
	SoftwareAssessment          Capability                  `json:"softwareAssessment"`
	CredentialResolution        CredentialResolution        `json:"credentialResolution"`
}

type HTTP struct {
	ServiceActivityEnabled bool     `json:"serviceActivityEnabled"`
	Address                string   `json:"address"`
	PublicBaseURL          string   `json:"publicBaseUrl"`
	ReadTimeout            Duration `json:"readTimeout"`
	RequestTimeout         Duration `json:"requestTimeout"`
	WriteTimeout           Duration `json:"writeTimeout"`
	IdleTimeout            Duration `json:"idleTimeout"`
	ShutdownTimeout        Duration `json:"shutdownTimeout"`
	MaxBodyBytes           int64    `json:"maxBodyBytes"`
	CORSOrigins            []string `json:"corsOrigins,omitempty"`
}

type Database struct {
	Driver            string   `json:"driver"`
	DSN               string   `json:"dsn,omitempty"`
	MaxConnections    int32    `json:"maxConnections,omitempty"`
	MaxConnLifetime   Duration `json:"maxConnLifetime"`
	MaxConnIdleTime   Duration `json:"maxConnIdleTime"`
	HealthCheckPeriod Duration `json:"healthCheckPeriod"`
	MigrateOnStart    bool     `json:"migrateOnStart,omitempty"`
}

type Auth struct {
	Mode              string                     `json:"mode"`
	Issuer            string                     `json:"issuer,omitempty"`
	TrustBundleFile   string                     `json:"trustBundleFile,omitempty"`
	TrustBundleDigest string                     `json:"-"`
	Audience          string                     `json:"audience,omitempty"`
	TenancyMode       string                     `json:"tenancyMode,omitempty"`
	DedicatedTenantID string                     `json:"dedicatedTenantId,omitempty"`
	OrganizationClaim string                     `json:"organizationClaim,omitempty"`
	EnforceScopes     bool                       `json:"enforceScopes,omitempty"`
	DevelopmentTokens map[string]DevelopmentUser `json:"developmentTokens,omitempty"`
}

type DevelopmentUser struct {
	Subject        string                     `json:"subject"`
	TenantID       string                     `json:"tenantId"`
	ActorType      identity.ActorType         `json:"actorType,omitempty"`
	Label          string                     `json:"label,omitempty"`
	Description    string                     `json:"description,omitempty"`
	ProfileGroup   string                     `json:"profileGroup,omitempty"`
	Elevated       bool                       `json:"elevated,omitempty"`
	SeparateTenant bool                       `json:"separateTenant,omitempty"`
	RoleTemplates  []string                   `json:"roleTemplates,omitempty"`
	Roles          []identity.Role            `json:"roles,omitempty"`
	Namespaces     map[string][]identity.Role `json:"namespaces,omitempty"`
}

// IdentityType keeps classification independent from an explicitly selected
// empty template. Omission retains the established development inference.
func (u DevelopmentUser) IdentityType() identity.ActorType {
	if u.ActorType != "" {
		return u.ActorType
	}
	for _, role := range u.Roles {
		if role == identity.RoleServiceClient {
			return identity.ActorService
		}
	}
	for _, key := range u.RoleTemplates {
		if key == "service_client" {
			return identity.ActorService
		}
	}
	return identity.ActorHuman
}

// SeedRoleTemplates gives an otherwise grant-free development declaration the
// explicit empty membership template in both stores. Classification is resolved
// from the original declaration, so this does not turn a human into a service.
func (u DevelopmentUser) SeedRoleTemplates() []string {
	if len(u.Roles) == 0 && len(u.RoleTemplates) == 0 && len(u.Namespaces) == 0 {
		return []string{"service_client"}
	}
	return append([]string(nil), u.RoleTemplates...)
}

// Jobs controls new durable Job budgets and future automatic retry windows.
type Jobs struct {
	MaxAttempts  int      `json:"maxAttempts"`
	RetryCeiling Duration `json:"retryCeiling"`
}

type Worker struct {
	PollInterval                Duration `json:"pollInterval"`
	DependencyRetryInitialDelay Duration `json:"dependencyRetryInitialDelay"`
	DependencyRetryCeiling      Duration `json:"dependencyRetryCeiling"`
	LeaseDuration               Duration `json:"leaseDuration"`
	Concurrency                 int      `json:"concurrency"`
	SourceMaintenanceInterval   Duration `json:"sourceMaintenanceInterval"`
	HousekeepingInterval        Duration `json:"housekeepingInterval"`
	TerminalJobRetention        Duration `json:"terminalJobRetention"`
	AcknowledgedOutboxRetention Duration `json:"acknowledgedOutboxRetention"`
	OperationsAddress           string   `json:"operationsAddress,omitempty"`
}

type Federation struct {
	RequestTimeout        Duration         `json:"requestTimeout"`
	MaxResponseBytes      int64            `json:"maxResponseBytes"`
	MaxRedirects          int              `json:"maxRedirects"`
	MaxFanout             int              `json:"maxFanout"`
	MaxLivePagesPerSource int              `json:"maxLivePagesPerSource"`
	MaxHops               int              `json:"maxHops"`
	PolicyFile            string           `json:"policyFile,omitempty"`
	Policy                FederationPolicy `json:"-"`
	PolicyDigest          string           `json:"-"`
}

type FederationPolicy struct {
	SchemaVersion                 int                        `json:"schemaVersion"`
	PolicyID                      string                     `json:"policyId"`
	Revision                      int64                      `json:"revision"`
	Public                        FederationPublicPolicy     `json:"public"`
	AdditionalDeniedOrigins       []string                   `json:"additionalDeniedOrigins,omitempty"`
	AdditionalDeniedAddressRanges []string                   `json:"additionalDeniedAddressRanges,omitempty"`
	PrivateProfiles               []FederationPrivateProfile `json:"privateProfiles,omitempty"`
}

type FederationPublicPolicy struct {
	Mode    string   `json:"mode"`
	Origins []string `json:"origins,omitempty"`
}

type FederationPrivateProfile struct {
	Name              string   `json:"name"`
	Origins           []string `json:"origins"`
	AddressRanges     []string `json:"addressRanges"`
	AssignedTenantIDs []string `json:"assignedTenantIds"`
	TrustBundleFile   string   `json:"trustBundleFile,omitempty"`
	TrustBundlePEM    []byte   `json:"-"`
	TransportRef      string   `json:"transportRef"`
}

type Discovery struct {
	Hosts []DiscoveryHost `json:"hosts,omitempty"`
}

type DiscoveryHost struct {
	Host            string `json:"host"`
	TenantID        string `json:"tenantId"`
	Mode            string `json:"mode"`
	PublisherDomain string `json:"publisherDomain"`
}

type Publication struct {
	ARDProfile       string `json:"ardProfile"`
	AICatalogProfile string `json:"aiCatalogProfile"`
}

type Webhooks struct {
	PolicyFile   string           `json:"policyFile,omitempty"`
	Policy       FederationPolicy `json:"-"`
	PolicyDigest string           `json:"-"`
	RetryCeiling Duration         `json:"retryCeiling,omitempty"`
	Enabled      bool             `json:"enabled"`
	MaxAttempts  int              `json:"maxAttempts,omitempty"`
	Timeout      Duration         `json:"timeout,omitempty"`
	PollInterval Duration         `json:"pollInterval,omitempty"`
}

type Capability struct {
	Enabled bool `json:"enabled"`
}

type CredentialResolution struct {
	Enabled   bool   `json:"enabled"`
	Directory string `json:"directory,omitempty"`
}

// LoadFor overlays an optional JSON file and explicit environment allowlist on
// defaults, loads applicable scoped policies, then validates the selected role.
// It may read policy/trust files but never discovers an env file or connects to
// PostgreSQL or a provider; dependency admission belongs to runtime startup.
func LoadFor(path string, role Role) (Config, error) {
	if !role.Valid() {
		return Config{}, fmt.Errorf("config: unsupported role %q", role)
	}
	cfg := Defaults()
	if path != "" {
		if err := decodeFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnvironment(&cfg, os.LookupEnv); err != nil {
		return Config{}, err
	}
	if role != RoleMigration {
		if err := loadFederationPolicy(&cfg); err != nil {
			return Config{}, err
		}
		if err := loadWebhookPolicy(&cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.ValidateFor(role); err != nil {
		return Config{}, err
	}
	if role != RoleMigration {
		if err := loadTrustVerification(&cfg); err != nil {
			return Config{}, err
		}
		if err := loadDemoArchives(&cfg); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// decodeFile overlays the selected JSON file onto cfg, rejecting nulls, duplicate
// keys, unknown fields and trailing values. Callers discard cfg after any failure.
func decodeFile(path string, cfg *Config) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator explicitly selects the configuration path.
	if err != nil {
		return safeerr.Wrap(fmt.Errorf("read config: %w", err), "cannot read configuration file")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return safeerr.Wrap(fmt.Errorf("decode config: %w", err), "invalid configuration JSON; check field names, types and duplicate keys")
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return safeerr.Wrap(fmt.Errorf("decode config: %w", err), "invalid configuration JSON")
	}
	if containsNull(document) {
		return safeerr.New("decode config: null values are not supported")
	}
	if err := jsondoc.Decode(raw, cfg); err != nil {
		return safeerr.Wrap(fmt.Errorf("decode config: %w", err), "invalid configuration JSON; check field names, types and duplicate keys")
	}
	return nil
}

// containsNull checks decoded JSON recursively so null cannot silently clear defaults.
func containsNull(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range typed {
			if containsNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsNull(item) {
				return true
			}
		}
	}
	return false
}
