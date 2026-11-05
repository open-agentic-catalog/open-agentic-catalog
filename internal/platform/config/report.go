// Diagnostic configuration types project selected values and presence indicators
// without serializing runtime secrets, credential paths or tracing destinations.

package config

import "github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"

type Report struct {
	Ratings                     Ratings                     `json:"ratings"`
	DemoArchivesEnabled         bool                        `json:"demoArchivesEnabled"`
	LogLevel                    string                      `json:"logLevel"`
	LogFormat                   string                      `json:"logFormat"`
	Tracing                     TracingReport               `json:"tracing"`
	Environment                 string                      `json:"environment"`
	Profile                     string                      `json:"profile"`
	ServiceName                 string                      `json:"serviceName"`
	Validation                  catalog.DiagnosticLimits    `json:"validation"`
	MCP                         MCP                         `json:"mcp"`
	HTTP                        HTTP                        `json:"http"`
	Database                    DatabaseReport              `json:"database"`
	Auth                        AuthReport                  `json:"auth"`
	Jobs                        Jobs                        `json:"jobs"`
	Worker                      Worker                      `json:"worker"`
	Federation                  FederationReport            `json:"federation"`
	Discovery                   Discovery                   `json:"discovery"`
	Publication                 Publication                 `json:"publication"`
	Webhooks                    WebhooksReport              `json:"webhooks"`
	SemanticSearch              Capability                  `json:"semanticSearch"`
	ListingMedia                ListingMediaReport          `json:"listingMedia"`
	TrustVerification           TrustVerificationReport     `json:"trustVerification"`
	PublisherDomainVerification PublisherDomainVerification `json:"publisherDomainVerification"`
	ManagedMedia                Capability                  `json:"managedMedia"`
	SoftwareAssessment          Capability                  `json:"softwareAssessment"`
	CredentialResolution        Capability                  `json:"credentialResolution"`
}

// TracingReport omits endpoint, installation, credentials and all input paths.
type TracingReport struct {
	Enabled                     bool     `json:"enabled"`
	EndpointConfigured          bool     `json:"endpointConfigured"`
	HeadersFileConfigured       bool     `json:"headersFileConfigured"`
	PrivateTrustConfigured      bool     `json:"privateTrustConfigured"`
	ClientCertificateConfigured bool     `json:"clientCertificateConfigured"`
	SampleRatio                 float64  `json:"sampleRatio"`
	TrustRemoteSampling         bool     `json:"trustRemoteSampling"`
	QueueSize                   int      `json:"queueSize"`
	BatchSize                   int      `json:"batchSize"`
	ExportTimeout               Duration `json:"exportTimeout"`
}

type WebhooksReport struct {
	Enabled              bool     `json:"enabled"`
	MaxAttempts          int      `json:"maxAttempts"`
	Timeout              Duration `json:"timeout"`
	PollInterval         Duration `json:"pollInterval"`
	RetryCeiling         Duration `json:"retryCeiling"`
	PolicyFileConfigured bool     `json:"policyFileConfigured"`
	PolicyDigest         string   `json:"policyDigest,omitempty"`
	PrivateProfileCount  int      `json:"privateProfileCount"`
}

type DatabaseReport struct {
	Driver            string   `json:"driver"`
	DSNConfigured     bool     `json:"dsnConfigured"`
	MaxConnections    int32    `json:"maxConnections"`
	MaxConnLifetime   Duration `json:"maxConnLifetime"`
	MaxConnIdleTime   Duration `json:"maxConnIdleTime"`
	HealthCheckPeriod Duration `json:"healthCheckPeriod"`
}

type AuthReport struct {
	Mode                     string `json:"mode"`
	Issuer                   string `json:"issuer,omitempty"`
	TrustBundleConfigured    bool   `json:"trustBundleConfigured"`
	TrustBundleDigest        string `json:"trustBundleDigest,omitempty"`
	Audience                 string `json:"audience,omitempty"`
	TenancyMode              string `json:"tenancyMode,omitempty"`
	DedicatedTenantID        string `json:"dedicatedTenantId,omitempty"`
	OrganizationClaim        string `json:"organizationClaim,omitempty"`
	EnforceScopes            bool   `json:"enforceScopes"`
	DevelopmentIdentityCount int    `json:"developmentIdentityCount,omitempty"`
}

type FederationReport struct {
	RequestTimeout        Duration `json:"requestTimeout"`
	MaxResponseBytes      int64    `json:"maxResponseBytes"`
	MaxRedirects          int      `json:"maxRedirects"`
	MaxFanout             int      `json:"maxFanout"`
	MaxLivePagesPerSource int      `json:"maxLivePagesPerSource"`
	MaxHops               int      `json:"maxHops"`
	PolicyID              string   `json:"policyId"`
	PolicyRevision        int64    `json:"policyRevision"`
	PolicyDigest          string   `json:"policyDigest"`
	PublicMode            string   `json:"publicMode"`
	PublicOriginCount     int      `json:"publicOriginCount"`
	PrivateProfileCount   int      `json:"privateProfileCount"`
}

// Report builds the safe configuration projection without reading injected files.
// Credential values, development tokens and database DSNs never enter the result.
func (c Config) Report() Report {
	trustReport := c.TrustVerification.Report()
	trustReport.PolicyDigest = c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest)
	return Report{
		DemoArchivesEnabled: c.DemoArchives.Origin != "",
		LogLevel:            c.LogLevel, LogFormat: c.LogFormat,
		Tracing: TracingReport{
			Enabled: c.Tracing.Enabled, EndpointConfigured: c.Tracing.Endpoint != "",
			HeadersFileConfigured: c.Tracing.HeadersFile != "", PrivateTrustConfigured: c.Tracing.CAFile != "",
			ClientCertificateConfigured: c.Tracing.ClientCertificateFile != "", SampleRatio: c.Tracing.SampleRatio,
			TrustRemoteSampling: c.Tracing.TrustRemoteSampling, QueueSize: c.Tracing.QueueSize,
			BatchSize: c.Tracing.BatchSize, ExportTimeout: c.Tracing.ExportTimeout,
		},
		Environment: c.Environment,
		Profile:     c.Profile,
		ServiceName: c.ServiceName,
		Validation:  c.Validation, HTTP: c.HTTP, MCP: c.MCP,
		Database: DatabaseReport{
			Driver:            c.Database.Driver,
			DSNConfigured:     c.Database.DSN != "",
			MaxConnections:    c.Database.MaxConnections,
			MaxConnLifetime:   c.Database.MaxConnLifetime,
			MaxConnIdleTime:   c.Database.MaxConnIdleTime,
			HealthCheckPeriod: c.Database.HealthCheckPeriod,
		},
		Auth: AuthReport{
			Mode:                     c.Auth.Mode,
			Issuer:                   c.Auth.Issuer,
			TrustBundleConfigured:    c.Auth.TrustBundleFile != "",
			TrustBundleDigest:        c.Auth.TrustBundleDigest,
			Audience:                 c.Auth.Audience,
			TenancyMode:              c.Auth.TenancyMode,
			DedicatedTenantID:        c.Auth.DedicatedTenantID,
			OrganizationClaim:        c.Auth.OrganizationClaim,
			EnforceScopes:            c.Auth.EnforceScopes,
			DevelopmentIdentityCount: len(c.Auth.DevelopmentTokens),
		},
		Jobs:   c.Jobs,
		Worker: c.Worker,
		Federation: FederationReport{
			RequestTimeout:        c.Federation.RequestTimeout,
			MaxResponseBytes:      c.Federation.MaxResponseBytes,
			MaxRedirects:          c.Federation.MaxRedirects,
			MaxFanout:             c.Federation.MaxFanout,
			MaxLivePagesPerSource: c.Federation.MaxLivePagesPerSource,
			MaxHops:               c.Federation.MaxHops,
			PolicyID:              c.Federation.Policy.PolicyID,
			PolicyRevision:        c.Federation.Policy.Revision,
			PolicyDigest:          c.Federation.PolicyDigest,
			PublicMode:            c.Federation.Policy.Public.Mode,
			PublicOriginCount:     len(c.Federation.Policy.Public.Origins),
			PrivateProfileCount:   len(c.Federation.Policy.PrivateProfiles),
		},
		Discovery:                   c.Discovery,
		Publication:                 c.Publication,
		Webhooks:                    WebhooksReport{Enabled: c.Webhooks.Enabled, MaxAttempts: c.Webhooks.MaxAttempts, Timeout: c.Webhooks.Timeout, PollInterval: c.Webhooks.PollInterval, RetryCeiling: c.Webhooks.RetryCeiling, PolicyFileConfigured: c.Webhooks.PolicyFile != "", PolicyDigest: c.Webhooks.PolicyDigest, PrivateProfileCount: len(c.Webhooks.Policy.PrivateProfiles)},
		SemanticSearch:              Capability{Enabled: c.SemanticSearch.Enabled},
		Ratings:                     c.Ratings,
		ListingMedia:                c.listingMediaReport(),
		TrustVerification:           trustReport,
		PublisherDomainVerification: c.PublisherDomainVerification,
		ManagedMedia:                c.ManagedMedia,
		SoftwareAssessment:          c.SoftwareAssessment,
		CredentialResolution:        Capability{Enabled: c.CredentialResolution.Enabled},
	}
}
