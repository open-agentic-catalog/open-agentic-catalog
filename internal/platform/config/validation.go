// Configuration validation keeps role requirements, deployment bounds and
// unsupported-provider rejection at startup, before constructing dependencies.

package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const (
	minimumHTTPRequestTimeout = time.Second
	maximumHTTPRequestTimeout = 20 * time.Second
	commandCleanupAllowance   = 5 * time.Second
	responseWriteMargin       = time.Second
)

// Validate checks the combined in-memory development role; split processes must
// use ValidateFor so unrelated listener requirements do not become dependencies.
func (c Config) Validate() error {
	return c.ValidateFor(RoleAll)
}

// ValidateFor checks common and role-specific invariants without opening files or
// connecting to services. Migration validation requires only its database inputs.
func (c Config) ValidateFor(role Role) error {
	if !role.Valid() {
		return fmt.Errorf("config: unsupported role %q", role)
	}
	if err := c.validateDemoArchives(); err != nil {
		return err
	}
	if err := diagnostics.ValidateLogging(c.LogLevel, c.LogFormat); err != nil {
		return err
	}
	if err := c.Validation.Validate(); err != nil {
		return safeerr.New("config: validation requires maxFindings 1-512 and maxDiagnosticBytes 1024-262144")
	}
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		return safeerr.New("config: environment must be development, test, or production")
	}
	if strings.TrimSpace(c.Profile) == "" || strings.TrimSpace(c.ServiceName) == "" {
		return safeerr.New("config: profile and serviceName are required")
	}
	if c.Environment == "production" && c.Profile == "local" {
		return safeerr.New("config: production requires a non-local profile")
	}
	if err := validateDatabase(c.Database, role); err != nil {
		return err
	}
	if err := c.PublisherDomainVerification.Policy().Validate(); err != nil {
		return safeerr.New("config: publisherDomainVerification has an invalid mode or duration")
	}
	if role == RoleMigration {
		return nil
	}
	if c.Ratings.MinimumSharedCount < 5 || c.Ratings.MinimumSharedCount > 1000 {
		return safeerr.New("config: ratings.minimumSharedCount must be between 5 and 1000")
	}
	if c.Jobs.MaxAttempts < 1 || c.Jobs.MaxAttempts > 20 {
		return safeerr.New("config: jobs.maxAttempts must be between 1 and 20")
	}
	if c.Jobs.RetryCeiling.Duration < 5*time.Second || c.Jobs.RetryCeiling.Duration > time.Hour {
		return safeerr.New("config: jobs.retryCeiling must be between 5s and 1h")
	}
	if err := c.ListingMedia.Validate(c.Federation.Policy); err != nil {
		return err
	}
	if err := c.TrustVerification.Validate(c.Federation.Policy, role, c.Worker.LeaseDuration.Duration); err != nil {
		return err
	}
	if err := c.Tracing.Validate(); err != nil {
		return err
	}
	if err := c.SemanticSearch.Validate(); err != nil {
		return err
	}
	if c.SemanticSearch.Enabled {
		if c.Database.Driver != "postgres" {
			return safeerr.New("config: semantic search requires PostgreSQL")
		}
		if role == RoleAPI && c.SemanticSearch.QueryTimeout.Duration+250*time.Millisecond >= c.HTTP.RequestTimeout.Duration {
			return safeerr.New("config: semantic query timeout must leave time for lexical fallback")
		}
		if role == RoleWorker && c.SemanticSearch.BatchTimeout.Duration >= c.Worker.LeaseDuration.Duration/2 {
			return safeerr.New("config: semantic batch timeout must be shorter than half the worker lease")
		}
	}
	if err := c.validateApplication(role); err != nil {
		return err
	}
	return nil
}

// validateDatabase enforces explicit migration authority, split PostgreSQL roles,
// required connection input, pool-size ceiling and positive pool timings.
func validateDatabase(database Database, role Role) error {
	if database.Driver != "memory" && database.Driver != "postgres" {
		return safeerr.New("config: database.driver must be memory or postgres")
	}
	if role == RoleMigration && database.Driver != "postgres" {
		return safeerr.New("config: migrations require database.driver=postgres")
	}
	if role == RoleAll && database.Driver == "postgres" {
		return safeerr.New("config: PostgreSQL requires separate api and worker processes; the all role is limited to memory-backed development")
	}
	if database.Driver == "postgres" && strings.TrimSpace(database.DSN) == "" {
		return safeerr.New("config: a PostgreSQL DSN is required")
	}
	if database.MaxConnections < 0 || database.MaxConnections > 500 {
		return safeerr.New("config: database.maxConnections must be between 0 and 500")
	}
	if database.MaxConnLifetime.Duration <= 0 {
		return safeerr.New("config: database.maxConnLifetime must be positive")
	}
	if database.MaxConnIdleTime.Duration <= 0 {
		return safeerr.New("config: database.maxConnIdleTime must be positive")
	}
	if database.HealthCheckPeriod.Duration <= 0 {
		return safeerr.New("config: database.healthCheckPeriod must be positive")
	}
	if database.MigrateOnStart {
		return safeerr.New("config: application-startup migrations are unsupported; use the separate catalogctl migration command")
	}
	return nil
}

// validateApplication applies only the dependencies and listeners needed by the
// selected process role while keeping unimplemented capabilities fail-closed.
func (c Config) validateApplication(role Role) error {
	if err := c.validatePublicBaseURL(); err != nil {
		return err
	}
	if c.HTTP.ShutdownTimeout.Duration <= 0 {
		return safeerr.New("config: http.shutdownTimeout must be positive for service roles")
	}
	if role == RoleAPI || role == RoleAll {
		if err := c.validateHTTP(); err != nil {
			return err
		}
		if err := c.MCP.Validate(c.Environment == "production"); err != nil {
			return err
		}
		if err := c.validateAuth(); err != nil {
			return err
		}
	}
	if (role == RoleAPI || role == RoleWorker || role == RoleAll) && (c.Worker.SourceMaintenanceInterval.Duration < time.Minute || c.Worker.SourceMaintenanceInterval.Duration > 24*time.Hour) {
		return safeerr.New("config: worker.sourceMaintenanceInterval must be between one minute and twenty-four hours")
	}
	if role == RoleWorker || role == RoleAll {
		if c.Worker.DependencyRetryInitialDelay.Duration < time.Second || c.Worker.DependencyRetryCeiling.Duration > 5*time.Minute || c.Worker.DependencyRetryInitialDelay.Duration > c.Worker.DependencyRetryCeiling.Duration {
			return safeerr.New("config: worker dependency retry delays must satisfy 1s <= initial <= ceiling <= 5m")
		}
		if c.Worker.Concurrency < 1 || c.Worker.Concurrency > 128 || c.Worker.PollInterval.Duration <= 0 || c.Worker.LeaseDuration.Duration <= 0 || c.Worker.SourceMaintenanceInterval.Duration < time.Minute || c.Worker.SourceMaintenanceInterval.Duration > 24*time.Hour ||
			c.Worker.HousekeepingInterval.Duration < time.Minute || c.Worker.HousekeepingInterval.Duration > time.Hour ||
			c.Worker.TerminalJobRetention.Duration < 24*time.Hour || c.Worker.TerminalJobRetention.Duration > 2160*time.Hour ||
			c.Worker.AcknowledgedOutboxRetention.Duration < time.Hour || c.Worker.AcknowledgedOutboxRetention.Duration > 720*time.Hour {
			return safeerr.New("config: worker concurrency and durations are invalid")
		}
		if err := validateListenerAddress("worker.operationsAddress", c.Worker.OperationsAddress, c.Environment != "production"); err != nil {
			return err
		}
		if role == RoleAll && listenerAddressesConflict(c.Worker.OperationsAddress, c.HTTP.Address) {
			return safeerr.New("config: worker operations and API listeners must use different addresses")
		}
	}
	if c.Federation.RequestTimeout.Duration <= 0 || c.Federation.RequestTimeout.Duration > 5*time.Second || c.Federation.MaxResponseBytes < 1024 || c.Federation.MaxResponseBytes > 64<<20 || c.Federation.MaxRedirects < 0 || c.Federation.MaxRedirects > 10 || c.Federation.MaxFanout < 1 || c.Federation.MaxFanout > 12 || c.Federation.MaxLivePagesPerSource < 1 || c.Federation.MaxLivePagesPerSource > 5 || c.Federation.MaxHops < 0 || c.Federation.MaxHops > 2 {
		return safeerr.New("config: federation budgets are invalid")
	}
	if c.Federation.Policy.SchemaVersion != 1 || c.Federation.Policy.PolicyID == "" || c.Federation.Policy.Revision <= 0 || !validConfigurationDigest(c.Federation.PolicyDigest) {
		return safeerr.New("config: resolved federation policy identity is incomplete")
	}
	if c.CredentialResolution.Enabled {
		if !filepath.IsAbs(c.CredentialResolution.Directory) || filepath.Clean(c.CredentialResolution.Directory) != c.CredentialResolution.Directory {
			return safeerr.New("config: enabled credential resolution requires an absolute clean directory")
		}
	} else if c.CredentialResolution.Directory != "" {
		return safeerr.New("config: disabled credential resolution cannot retain a provider directory")
	}
	if err := c.validateDiscovery(); err != nil {
		return err
	}
	if c.Webhooks.Enabled && (c.Webhooks.MaxAttempts < 1 || c.Webhooks.MaxAttempts > 20 || c.Webhooks.Timeout.Duration <= 0 || c.Webhooks.PollInterval.Duration <= 0) {
		return safeerr.New("config: webhook retry and timing settings are invalid")
	}
	if c.Webhooks.Enabled && (role == RoleWorker || role == RoleAll) && c.Webhooks.Timeout.Duration >= c.Worker.LeaseDuration.Duration {
		return safeerr.New("config: webhook timeout must be shorter than the worker lease")
	}
	if c.Webhooks.Enabled && (c.Webhooks.RetryCeiling.Duration < 5*time.Second || c.Webhooks.RetryCeiling.Duration > time.Hour) {
		return safeerr.New("config: webhook retry ceiling must be between 5s and 1h")
	}
	if c.Publication.ARDProfile != activeARDProfileID || c.Publication.AICatalogProfile != activeAICatalogProfileID {
		return safeerr.New(fmt.Sprintf("config: publication profiles must be %q and %q", activeARDProfileID, activeAICatalogProfileID))
	}
	for _, capability := range []struct {
		name    string
		enabled bool
	}{
		{name: "managed media", enabled: c.ManagedMedia.Enabled},
		{name: "software assessment", enabled: c.SoftwareAssessment.Enabled},
	} {
		if capability.enabled {
			return safeerr.New(fmt.Sprintf("config: %s is not implemented and must remain disabled", capability.name))
		}
	}
	if c.Environment == "production" {
		if c.Database.Driver != "postgres" {
			return safeerr.New("config: production requires PostgreSQL")
		}
		if (role == RoleAPI || role == RoleAll) && c.Auth.Mode != "oidc" {
			return safeerr.New("config: the production API requires OIDC authentication")
		}
	}
	return nil
}

// validConfigurationDigest requires canonical lowercase SHA-256 text for
// comparisons with retained policy provenance.
func validConfigurationDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// validateHTTP checks listener/body bounds, exact CORS origins and enough outer
// timeout for command cleanup and response completion.
func (c Config) validateHTTP() error {
	if c.HTTP.Address == "" {
		return safeerr.New("config: http.address is required for the API role")
	}
	_, port, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil {
		return safeerr.New("config: http.address must contain a host and numeric port")
	}
	if value, err := strconv.ParseUint(port, 10, 16); err != nil || value == 0 && c.Environment == "production" {
		return safeerr.New("config: http.address port is invalid")
	}
	if c.HTTP.MaxBodyBytes < 1024 || c.HTTP.MaxBodyBytes > 64<<20 {
		return safeerr.New("config: http.maxBodyBytes must be between 1 KiB and 64 MiB")
	}
	if c.HTTP.ReadTimeout.Duration <= 0 || c.HTTP.WriteTimeout.Duration <= 0 || c.HTTP.IdleTimeout.Duration <= 0 || c.HTTP.ShutdownTimeout.Duration <= 0 {
		return safeerr.New("config: all HTTP timeouts must be positive")
	}
	if c.HTTP.RequestTimeout.Duration < minimumHTTPRequestTimeout || c.HTTP.RequestTimeout.Duration > maximumHTTPRequestTimeout {
		return safeerr.New("config: http.requestTimeout must be between 1s and 20s")
	}
	minimumOuterTimeout := c.HTTP.RequestTimeout.Duration + commandCleanupAllowance + responseWriteMargin
	if c.HTTP.WriteTimeout.Duration < minimumOuterTimeout {
		return safeerr.New("config: http.writeTimeout must allow requestTimeout, command cleanup, and response completion")
	}
	if c.HTTP.ShutdownTimeout.Duration < minimumOuterTimeout {
		return safeerr.New("config: http.shutdownTimeout must allow requestTimeout, command cleanup, and response completion")
	}
	seenOrigins := make(map[string]struct{}, len(c.HTTP.CORSOrigins))
	for _, origin := range c.HTTP.CORSOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || !validURLHost(parsed) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin == "*" {
			return safeerr.New("config: each CORS origin must be an exact HTTP or HTTPS origin")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" || c.Environment == "production" && parsed.Scheme != "https" {
			return safeerr.New("config: CORS origins must use HTTP or HTTPS, and HTTPS in production")
		}
		if _, exists := seenOrigins[origin]; exists {
			return safeerr.New("config: CORS origins must be unique")
		}
		seenOrigins[origin] = struct{}{}
	}
	return nil
}

// validateListenerAddress requires a numeric host/port pair and permits ephemeral
// port zero only when explicitly requested.
func validateListenerAddress(name, address string, allowZero bool) error {
	if address == "" {
		return safeerr.New(fmt.Sprintf("config: %s is required", name))
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return safeerr.New(fmt.Sprintf("config: %s must contain a host and numeric port", name))
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 && !allowZero {
		return safeerr.New(fmt.Sprintf("config: %s port is invalid", name))
	}
	return nil
}

// listenerAddressesConflict detects same-port overlap from identical or wildcard
// bind addresses after syntax validation.
func listenerAddressesConflict(first, second string) bool {
	firstHost, firstPort, firstErr := net.SplitHostPort(first)
	secondHost, secondPort, secondErr := net.SplitHostPort(second)
	if firstErr != nil || secondErr != nil || firstPort != secondPort {
		return false
	}
	wildcard := func(host string) bool {
		return host == "" || host == "0.0.0.0" || host == "::"
	}
	return firstHost == secondHost || wildcard(firstHost) || wildcard(secondHost)
}

// validatePublicBaseURL requires an HTTP(S) origin with no credentials or non-root
// path and requires HTTPS in production.
func (c Config) validatePublicBaseURL() error {
	base, err := url.Parse(c.HTTP.PublicBaseURL)
	if err != nil || !validURLHost(base) || base.Scheme != "http" && base.Scheme != "https" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return safeerr.New("config: http.publicBaseUrl must be an absolute HTTP or HTTPS origin")
	}
	if c.Environment == "production" && base.Scheme != "https" {
		return safeerr.New("config: production publicBaseUrl must use HTTPS")
	}
	return nil
}

// validateAuth keeps development identities and production OIDC tenancy as
// disjoint configuration shapes; neither mode inherits fields from the other.
func (c Config) validateAuth() error {
	if c.Auth.Mode != "development" && c.Auth.Mode != "oidc" {
		return safeerr.New("config: auth.mode must be development or oidc")
	}
	if c.Auth.Mode == "oidc" {
		if path := c.Auth.TrustBundleFile; path != "" && (strings.TrimSpace(path) != path || strings.ContainsAny(path, "\x00\r\n") || filepath.Clean(path) != path || c.Environment == "production" && !filepath.IsAbs(path)) {
			return safeerr.New("config: auth.trustBundleFile must be a clean file path, absolute in production")
		}
		if strings.TrimSpace(c.Auth.Issuer) == "" || strings.TrimSpace(c.Auth.Issuer) != c.Auth.Issuer || len(c.Auth.Issuer) > 2048 || strings.TrimSpace(c.Auth.Audience) == "" || strings.TrimSpace(c.Auth.Audience) != c.Auth.Audience || len(c.Auth.Audience) > 512 || strings.ContainsAny(c.Auth.Audience, "\x00\r\n") || len(c.Auth.DevelopmentTokens) != 0 {
			return safeerr.New("config: OIDC issuer and audience are required and development identities are forbidden")
		}
		issuer, err := url.Parse(c.Auth.Issuer)
		if err != nil || !validURLHost(issuer) || issuer.Scheme != "http" && issuer.Scheme != "https" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || c.Environment == "production" && issuer.Scheme != "https" {
			return safeerr.New("config: OIDC issuer must be an absolute HTTP or HTTPS URL, and HTTPS in production")
		}
		switch c.Auth.TenancyMode {
		case "dedicated":
			if !identity.ValidTenantID(c.Auth.DedicatedTenantID) || c.Auth.OrganizationClaim != "" {
				return safeerr.New("config: dedicated OIDC tenancy requires dedicatedTenantId and no organizationClaim")
			}
		case "shared":
			if c.Auth.DedicatedTenantID != "" || !validClaimName(c.Auth.OrganizationClaim) {
				return safeerr.New("config: shared OIDC tenancy requires organizationClaim and no dedicatedTenantId")
			}
		default:
			return safeerr.New("config: OIDC tenancyMode must be dedicated or shared")
		}
		return nil
	}
	if c.Auth.Issuer != "" || c.Auth.TrustBundleFile != "" || c.Auth.Audience != "" || c.Auth.TenancyMode != "" || c.Auth.DedicatedTenantID != "" || c.Auth.OrganizationClaim != "" || c.Auth.EnforceScopes {
		return safeerr.New("config: development auth cannot retain OIDC-only settings")
	}
	if len(c.Auth.DevelopmentTokens) == 0 {
		return safeerr.New("config: development auth requires at least one token")
	}
	principalTypes := map[[2]string]identity.ActorType{}
	for token, user := range c.Auth.DevelopmentTokens {
		if strings.TrimSpace(token) == "" || strings.TrimSpace(token) != token || len(token) > 512 || strings.ContainsAny(token, "\x00\r\n") || !identity.ValidSubject(user.Subject) || !identity.ValidTenantID(user.TenantID) {
			return safeerr.New("config: development identity is incomplete")
		}
		for _, role := range user.Roles {
			if !identity.ValidRole(role) {
				return safeerr.New("config: development identity role is invalid")
			}
		}
		if len(user.Label) > 100 || len(user.Description) > 300 || strings.IndexFunc(user.Label+user.Description, unicode.IsControl) >= 0 || user.Label != strings.TrimSpace(user.Label) || user.Description != strings.TrimSpace(user.Description) || len(user.RoleTemplates) > identity.MaxDevelopmentRoleTemplates || len(user.RoleTemplates) > 0 && len(user.Roles) > 0 {
			return safeerr.New("config: development identity presentation or templates are invalid")
		}
		seenTemplates := map[string]bool{}
		if user.ProfileGroup != "" && user.ProfileGroup != "bundle" && user.ProfileGroup != "template" {
			return safeerr.New("config: development identity profile group is invalid")
		}
		for _, key := range user.RoleTemplates {
			if _, ok := identity.BuiltinPermissions(key); !ok || seenTemplates[key] {
				return safeerr.New("config: development identity template is invalid or repeated")
			}
			seenTemplates[key] = true
		}
		// Development aliases all use the fixed development issuer and dedicated
		// tenant mapping. Token strings are never part of a principal or error.
		actorType := user.IdentityType()
		if !identity.ValidActorType(actorType) {
			return safeerr.New("config: development identity actor type is invalid")
		}
		principal := [2]string{strings.ToLower(user.TenantID), user.Subject}
		if previous, exists := principalTypes[principal]; exists && previous != actorType {
			return safeerr.New("config: development aliases for one principal have conflicting actor types")
		}
		principalTypes[principal] = actorType
		for namespaceID, roles := range user.Namespaces {
			if !identity.ValidTenantID(namespaceID) || len(roles) == 0 {
				return safeerr.New("config: development namespace assignment is invalid")
			}
			for _, role := range roles {
				if role != identity.RoleReader && role != identity.RoleContributor && role != identity.RoleReviewer && role != identity.RolePublisher {
					return safeerr.New("config: development namespace assignment role is invalid")
				}
			}
		}
	}
	return nil
}

// validateDiscovery bounds unique canonical host mappings and requires valid mode,
// tenant UUID and publisher domain for each.
func (c Config) validateDiscovery() error {
	if len(c.Discovery.Hosts) > 64 {
		return safeerr.New("config: at most 64 discovery hosts may be configured")
	}
	seen := make(map[string]struct{}, len(c.Discovery.Hosts))
	for _, mapping := range c.Discovery.Hosts {
		address := net.ParseIP(mapping.Host)
		if mapping.Host != strings.ToLower(mapping.Host) || strings.HasSuffix(mapping.Host, ".") || address != nil && mapping.Host != address.String() || address == nil && !validDNSName(mapping.Host) {
			return safeerr.New("config: each discovery host must be an exact canonical lower-case hostname or IP address without a port")
		}
		if _, exists := seen[mapping.Host]; exists {
			return safeerr.New("config: discovery hosts must be unique")
		}
		seen[mapping.Host] = struct{}{}
		if mapping.Mode != "disabled" && mapping.Mode != "anonymous" && mapping.Mode != "authenticated" {
			return safeerr.New("config: discovery host mode must be disabled, anonymous, or authenticated")
		}
		if !identity.ValidTenantID(mapping.TenantID) || !validDNSName(mapping.PublisherDomain) {
			return safeerr.New("config: each discovery host requires a tenant UUID and canonical publisher domain")
		}
	}
	return nil
}

// validClaimName accepts only the bounded ASCII claim-key grammar supported by
// configured tenancy lookup.
func validClaimName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		if character != '_' && character != '-' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}

// validURLHost accepts an IP literal or validated DNS name with an optional
// nonzero numeric port.
func validURLHost(value *url.URL) bool {
	hostname := value.Hostname()
	if hostname == "" || strings.HasSuffix(value.Host, ":") {
		return false
	}
	if net.ParseIP(hostname) == nil && !validDNSName(hostname) {
		return false
	}
	port := value.Port()
	if port == "" {
		return true
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsed > 0
}

// validDNSName checks lowercase DNS labels and rejects IP literals and numeric
// dotted strings that could create host ambiguity.
func validDNSName(value string) bool {
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return false
	}
	if strings.IndexFunc(value, func(character rune) bool { return character != '.' && (character < '0' || character > '9') }) == -1 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character < 'a' || character > 'z' {
				if character < '0' || character > '9' {
					if character != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}

// validNamespace permits bounded lowercase alphanumeric slugs with internal
// hyphens, excluding empty or edge-hyphen forms.
func validNamespace(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '-' {
					return false
				}
			}
		}
	}
	return true
}
