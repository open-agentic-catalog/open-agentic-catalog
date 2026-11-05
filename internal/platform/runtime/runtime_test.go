// Unit tests exercise isolated validation and controlled dependencies.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestPostgresCombinedRuntimeFailsBeforeConnecting(t *testing.T) {
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, Ratings: config.Defaults().Ratings,
		Environment: "test", Profile: "postgres-all", ServiceName: "open-agentic-catalog-postgres-all",
		HTTP:     config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(7 * time.Second), IdleTimeout: duration(time.Second), ShutdownTimeout: duration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database: config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001"},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Second), LeaseDuration: duration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   testDiscovery(),
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "postgres-all.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: "all", Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)}); err == nil {
		t.Fatal("combined PostgreSQL runtime succeeded")
	}
}

func TestRuntimeValidatesProductionSecretsBeforeOpeningDependencies(t *testing.T) {
	cfg := localRuntimeConfig()
	cfg.Environment = "production"
	cfg.Profile = "production-preflight-test"
	cfg.Database = config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod}
	cfg.Webhooks = config.Webhooks{RetryCeiling: config.Duration{Duration: time.Hour}, Enabled: true, MaxAttempts: 3, Timeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}}
	path := writeRuntimeConfig(t, cfg)
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY")
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY_FILE")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE")

	if _, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: config.RoleWorker, Logger: slog.New(slog.DiscardHandler)}); err == nil || !strings.Contains(err.Error(), "CATALOG_CURSOR_SIGNING_KEY") {
		t.Fatalf("missing cursor key preflight returned %v", err)
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: config.RoleWorker, Logger: slog.New(slog.DiscardHandler)}); err == nil || !strings.Contains(err.Error(), "CATALOG_WEBHOOK_ENCRYPTION_KEY") {
		t.Fatalf("missing webhook key preflight returned %v", err)
	}
}

func TestBuildRejectsLocalConfigurationAndCompositionFailures(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if _, err := Build(context.Background(), BuildOptions{Role: "invalid", Logger: logger}); err == nil {
		t.Fatal("unsupported runtime role was accepted")
	}
	if _, err := Build(context.Background(), BuildOptions{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Role: "worker", Logger: logger}); err == nil {
		t.Fatal("missing configuration file was accepted")
	}

	t.Run("cursor key", func(t *testing.T) {
		t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "not-base64")
		cfg := localRuntimeConfig()
		if _, err := Build(context.Background(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: "worker", Logger: logger}); err == nil {
			t.Fatal("invalid cursor signing key was accepted by runtime composition")
		}
	})

	t.Run("webhook key", func(t *testing.T) {
		t.Setenv("CATALOG_WEBHOOK_ENCRYPTION_KEY", "not-base64")
		cfg := localRuntimeConfig()
		cfg.Webhooks = config.Webhooks{RetryCeiling: config.Duration{Duration: time.Hour}, Enabled: true, MaxAttempts: 3, Timeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Millisecond}}
		if _, err := Build(context.Background(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: "worker", Logger: logger}); err == nil {
			t.Fatal("invalid webhook encryption key was accepted by runtime composition")
		}
	})
}

func TestFederationRoutingPolicyMapsOnlyNormalizedDeploymentTopology(t *testing.T) {
	configuration := localRuntimeConfig()
	configuration.Federation.RequestTimeout = config.Duration{Duration: 17 * time.Second}
	configuration.Federation.MaxResponseBytes = 5 << 20
	configuration.Federation.MaxRedirects = 4
	configuration.Federation.PolicyDigest = "sha256:" + strings.Repeat("a", 64)
	configuration.Federation.Policy = config.FederationPolicy{
		SchemaVersion:                 1,
		PolicyID:                      "enterprise",
		Revision:                      7,
		Public:                        config.FederationPublicPolicy{Mode: "allowlist", Origins: []string{"https://catalog.example:443"}},
		AdditionalDeniedOrigins:       []string{"https://denied.example:443"},
		AdditionalDeniedAddressRanges: []string{"169.254.0.0/16"},
		PrivateProfiles: []config.FederationPrivateProfile{{
			Name:              "private-catalogs",
			Origins:           []string{"https://catalog.internal:8443"},
			AddressRanges:     []string{"10.42.0.0/16", "fd00::/64"},
			AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000001"},
			TrustBundlePEM:    []byte("validated trust bytes"),
			TransportRef:      "direct",
		}},
	}
	for _, test := range []struct {
		environment        string
		allowHTTP          bool
		allowTestAddresses bool
	}{
		{environment: "production"},
		{environment: "development", allowHTTP: true},
		{environment: "test", allowHTTP: true, allowTestAddresses: true},
	} {
		t.Run(test.environment, func(t *testing.T) {
			configured := configuration
			configured.Environment = test.environment
			policy, err := federationRoutingPolicy(configured, "1.2.3")
			if err != nil {
				t.Fatal(err)
			}
			if policy.ID != "enterprise" || policy.Revision != 7 || policy.Digest != configuration.Federation.PolicyDigest || policy.UserAgent != "open-agentic-catalog/1.2.3" {
				t.Fatalf("routing policy identity = %#v", policy)
			}
			if len(policy.DeniedAddressPrefixes) != 1 || policy.DeniedAddressPrefixes[0].String() != "169.254.0.0/16" || len(policy.PrivateRoutes) != 1 || len(policy.PrivateRoutes[0].AddressPrefixes) != 2 || string(policy.PrivateRoutes[0].TrustBundlePEM) != "validated trust bytes" {
				t.Fatalf("routing policy topology = %#v", policy)
			}
			if policy.AllowHTTPForTests != test.allowHTTP || policy.PrivateRoutes[0].AllowTestAddresses != test.allowTestAddresses {
				t.Fatalf("routing permissions: HTTP=%t test addresses=%t; want %t, %t", policy.AllowHTTPForTests, policy.PrivateRoutes[0].AllowTestAddresses, test.allowHTTP, test.allowTestAddresses)
			}
			if policy.Timeout != 17*time.Second || policy.MaxResponseBytes != 5<<20 || policy.MaxRedirects != 4 {
				t.Fatalf("routing limits: timeout=%v response bytes=%d redirects=%d", policy.Timeout, policy.MaxResponseBytes, policy.MaxRedirects)
			}
		})
	}

	configuration.Federation.Policy.AdditionalDeniedAddressRanges = []string{"invalid"}
	if _, err := federationRoutingPolicy(configuration, "1.2.3"); err == nil {
		t.Fatal("invalid normalized denied range was accepted")
	}
	configuration.Federation.Policy.AdditionalDeniedAddressRanges = nil
	configuration.Federation.Policy.PrivateProfiles[0].AddressRanges = []string{"invalid"}
	if _, err := federationRoutingPolicy(configuration, "1.2.3"); err == nil {
		t.Fatal("invalid normalized private range was accepted")
	}
}

func localRuntimeConfig() config.Config {
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	const tenantID = "00000000-0000-7000-8000-000000000001"
	return config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(),
		Environment: "test",
		Profile:     "composition-test",
		ServiceName: "open-agentic-catalog-composition-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(7 * time.Second), IdleTimeout: duration(time.Second), ShutdownTimeout: duration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"worker": {Subject: "worker", TenantID: tenantID, Roles: []identity.Role{identity.RolePublisher}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   testDiscovery(),
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
}

func testDiscovery() config.Discovery {
	return config.Discovery{Hosts: []config.DiscoveryHost{{Host: "catalog.example.com", TenantID: "00000000-0000-7000-8000-000000000001", Mode: "anonymous", PublisherDomain: "catalog.example.com"}}}
}

func writeRuntimeConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProductionKeyMaterialMustBeExplicitAndValid(t *testing.T) {
	cfg := config.Config{Environment: "production", ServiceName: "catalog", Discovery: testDiscovery()}
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY")
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY_FILE")
	if _, err := config.CursorSigningKey(cfg); err == nil {
		t.Fatal("production cursor key default was accepted")
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "not-base64")
	if _, err := config.CursorSigningKey(cfg); err == nil {
		t.Fatal("invalid production cursor key was accepted")
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	key, err := config.CursorSigningKey(cfg)
	if err != nil || len(key) != 32 {
		t.Fatalf("valid production cursor key = %d bytes, %v", len(key), err)
	}
}

func TestRuntimeKeyPayloadAndActorHelpers(t *testing.T) {
	tenantID := "00000000-0000-7000-8000-000000000001"
	cfg := config.Config{Environment: "development", ServiceName: "catalog", Discovery: testDiscovery()}

	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY")
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY_FILE")
	developmentCursor, err := config.CursorSigningKey(cfg)
	if err != nil || len(developmentCursor) != 32 {
		t.Fatalf("development cursor key = %d bytes, %v", len(developmentCursor), err)
	}
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE")
	developmentWebhook, err := config.WebhookEncryptionKey(cfg)
	if err != nil || len(developmentWebhook) != 32 {
		t.Fatalf("development webhook key = %d bytes, %v", len(developmentWebhook), err)
	}
	if string(developmentCursor) == string(developmentWebhook) {
		t.Fatal("cursor and webhook development keys are not purpose-separated")
	}

	valid := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("CATALOG_WEBHOOK_ENCRYPTION_KEY", valid)
	if key, keyErr := config.WebhookEncryptionKey(cfg); keyErr != nil || len(key) != 32 {
		t.Fatalf("explicit webhook key = %d bytes, %v", len(key), keyErr)
	}
	for _, invalid := range []string{"not-base64", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		t.Setenv("CATALOG_WEBHOOK_ENCRYPTION_KEY", invalid)
		if _, keyErr := config.WebhookEncryptionKey(cfg); keyErr == nil {
			t.Fatalf("invalid webhook key %q was accepted", invalid)
		}
	}
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY")
	cfg.Environment = "production"
	if _, keyErr := config.WebhookEncryptionKey(cfg); keyErr == nil {
		t.Fatal("missing production webhook key was accepted")
	}

	var payload struct {
		Value string `json:"value"`
	}
	if err := jsonUnmarshal(nil, &payload); err == nil {
		t.Fatal("empty job payload was accepted")
	}
	if err := jsonUnmarshal([]byte(`{"value":`), &payload); err == nil {
		t.Fatal("malformed job payload was accepted")
	}
	if err := jsonUnmarshal([]byte(`{"value":"ok"}`), &payload); err != nil || payload.Value != "ok" {
		t.Fatalf("job payload = %#v, %v", payload, err)
	}
	if err := jsonUnmarshal([]byte(`{"value":"ok","extra":true}`), &payload); err == nil {
		t.Fatal("unknown job payload field was accepted")
	}
	if err := jsonUnmarshal([]byte(`{"value":"first","value":"second"}`), &payload); err == nil {
		t.Fatal("duplicate job payload field was accepted")
	}

	if ctx, err := workerActor(context.Background(), jobs.Job{TenantID: tenantID}, "forged-target"); err == nil || ctx != nil {
		t.Fatal("an unclaimed Job obtained internal worker authority")
	}

}

func TestSourceSyncFailureMappingsAreExplicit(t *testing.T) {
	tests := []struct {
		class   federation.SyncFailureClass
		job     jobs.FailureClass
		message string
	}{
		{class: federation.SyncFailureInvalidInput, job: jobs.FailureInvalidInput, message: "The source changed, was removed, or returned invalid content."},
		{class: federation.SyncFailurePolicy, job: jobs.FailurePolicyDenied, message: "The source is no longer permitted by federation policy."},
		{class: federation.SyncFailureAuthentication, job: jobs.FailureAuthentication, message: "Source authentication failed."},
		{class: federation.SyncFailureRateLimited, job: jobs.FailureRateLimited, message: "Source synchronization was rate limited."},
		{class: federation.SyncFailureTransient, job: jobs.FailureTransientUpstream, message: "Source synchronization did not complete within its safe bounds."},
		{class: federation.SyncFailurePermanent, job: jobs.FailurePermanentUpstream, message: "The source returned an unsupported permanent response."},
		{class: federation.SyncFailureDependency, job: jobs.FailureDependency, message: "A required source synchronization dependency is unavailable."},
		{class: federation.SyncFailureInternal, job: jobs.FailureInternal, message: "Source synchronization encountered an internal error."},
		{class: federation.SyncFailureCancelled, job: jobs.FailureCancelled, message: "Source synchronization was cancelled."},
	}
	for _, test := range tests {
		t.Run(string(test.class), func(t *testing.T) {
			if actual := jobFailureClass(test.class); actual != test.job {
				t.Fatalf("job failure class = %q, want %q", actual, test.job)
			}
			if actual := sourceSyncSafeError(test.class); actual != test.message {
				t.Fatalf("safe message = %q, want %q", actual, test.message)
			}
		})
	}

	unknown := federation.SyncFailureClass("future_class")
	if actual := jobFailureClass(unknown); actual != jobs.FailureTransientUpstream {
		t.Fatalf("unknown job failure class = %q", actual)
	}
	if actual := sourceSyncSafeError(unknown); actual != "Source synchronization did not complete within its safe bounds." {
		t.Fatalf("unknown safe message = %q", actual)
	}
}

type emptyPublicationRecords struct{}

func (emptyPublicationRecords) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	return nil, false, nil
}

func TestDiscoveryCompositionAndWithdrawalHelpers(t *testing.T) {
	const tenantOne = "00000000-0000-7000-8000-000000000001"
	const tenantTwo = "00000000-0000-7000-8000-000000000002"
	const namespaceOne = "00000000-0000-7000-8000-000000000011"
	const namespaceTwo = "00000000-0000-7000-8000-000000000012"
	configuration := config.Config{Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
		"z-human": {TenantID: tenantTwo, Subject: "z-human", Roles: []identity.Role{identity.RoleReader}},
		"b-human": {TenantID: tenantOne, Subject: "b-human", Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{
			namespaceOne: {identity.RoleContributor, identity.RoleReviewer},
			namespaceTwo: {identity.RoleReader},
		}},
		"a-service": {TenantID: tenantOne, Subject: "a-service", Roles: []identity.Role{identity.RoleServiceClient, identity.RoleReader}},
	}}}
	seeds := developmentIdentitySeeds(configuration)
	wantSeeds := []identity.Seed{
		{TenantID: tenantOne, Issuer: authn.DevelopmentIssuer, Subject: "a-service", ActorType: identity.ActorService, Roles: []identity.Role{identity.RoleServiceClient, identity.RoleReader}},
		{TenantID: tenantOne, Issuer: authn.DevelopmentIssuer, Subject: "b-human", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{
			namespaceOne: {identity.RoleContributor, identity.RoleReviewer},
			namespaceTwo: {identity.RoleReader},
		}},
		{TenantID: tenantTwo, Issuer: authn.DevelopmentIssuer, Subject: "z-human", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}},
	}
	if !reflect.DeepEqual(seeds, wantSeeds) {
		t.Fatalf("development identity seeds = %#v; want %#v", seeds, wantSeeds)
	}
	configuration.Auth.DevelopmentTokens["a-service"].Roles[0] = identity.RoleTenantAdmin
	if seeds[0].Roles[0] != identity.RoleServiceClient {
		t.Fatalf("seed roles alias configuration: %#v", seeds[0].Roles)
	}
	configuration.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"exact": {TenantID: tenantOne, Subject: "exact-service", RoleTemplates: []string{"service_client"}}}
	exactSeeds := developmentIdentitySeeds(configuration)
	if len(exactSeeds) != 1 || exactSeeds[0].ActorType != identity.ActorService || len(exactSeeds[0].RoleTemplates) != 1 || exactSeeds[0].RoleTemplates[0] != "service_client" || len(exactSeeds[0].Roles) != 0 {
		t.Fatalf("exact seeds %#v", exactSeeds)
	}
	configuration.Auth.DevelopmentTokens["exact"].RoleTemplates[0] = "reader"
	if exactSeeds[0].RoleTemplates[0] != "service_client" {
		t.Fatal("template seeds alias configuration")
	}
	configuration.Auth.Mode = "oidc"
	if seeds := developmentIdentitySeeds(configuration); seeds != nil {
		t.Fatalf("OIDC configuration produced development seeds: %#v", seeds)
	}

	for name, test := range map[string]struct {
		base string
		host string
		want string
	}{
		"invalid base":   {base: "://invalid", host: "catalog.example.com", want: "://invalid"},
		"relative base":  {base: "catalog.example.com", host: "public.example.com", want: "catalog.example.com"},
		"DNS":            {base: "https://catalog.example.com", host: "public.example.com", want: "https://public.example.com"},
		"DNS port":       {base: "https://catalog.example.com:8443", host: "public.example.com", want: "https://public.example.com:8443"},
		"IPv6":           {base: "https://catalog.example.com", host: "2001:db8::1", want: "https://[2001:db8::1]"},
		"IPv6 with port": {base: "https://catalog.example.com:8443", host: "2001:db8::1", want: "https://[2001:db8::1]:8443"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := discoveryBaseURL(test.base, test.host); got != test.want {
				t.Fatalf("discoveryBaseURL(%q, %q) = %q, want %q", test.base, test.host, got, test.want)
			}
		})
	}

	first := &publication.Manager{}
	second := &publication.Manager{}
	if selected := firstPublicationManager(map[string]*publication.Manager{"z.example.com": second, "a.example.com": first}); selected != first {
		t.Fatalf("first publication manager = %p, want %p", selected, first)
	}
	if selected := firstPublicationManager(nil); selected != nil {
		t.Fatalf("empty publication manager selection = %p", selected)
	}
	coordinator := &publicationWithdrawalCoordinator{managers: map[string]publicationBinding{
		"z.example.com":     {tenantID: tenantOne, manager: second},
		"a.example.com":     {tenantID: tenantOne, manager: first},
		"other.example.com": {tenantID: tenantTwo, manager: &publication.Manager{}},
	}}
	visited := make([]*publication.Manager, 0, 2)
	withdrawn, err := coordinator.withdraw(tenantOne, func(manager *publication.Manager) (bool, error) {
		visited = append(visited, manager)
		return manager == second, nil
	})
	if err != nil || len(visited) != 2 || visited[0] != first || visited[1] != second || len(withdrawn) != 1 || withdrawn[0] != "z.example.com" {
		t.Fatalf("ordered withdrawal = hosts %#v, visited %#v, error %v", withdrawn, visited, err)
	}
	coordinator.managers["zz.example.com"] = publicationBinding{tenantID: tenantOne, manager: &publication.Manager{}}
	for _, test := range []struct {
		name string
		fail *publication.Manager
		want []*publication.Manager
	}{
		{name: "first withdrawal fails", fail: first, want: []*publication.Manager{first}},
		{name: "later withdrawal fails", fail: second, want: []*publication.Manager{first, second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := errors.New("withdrawal failed")
			var calls []*publication.Manager
			hosts, err := coordinator.withdraw(tenantOne, func(manager *publication.Manager) (bool, error) {
				calls = append(calls, manager)
				if manager == test.fail {
					return false, failure
				}
				return true, nil
			})
			if !errors.Is(err, failure) || hosts != nil || !slices.Equal(calls, test.want) {
				t.Fatalf("failed withdrawal: hosts=%v error=%v calls=%v; want no hosts, original cause and calls=%v", hosts, err, calls, test.want)
			}
		})
	}

	key := sha256.Sum256([]byte("empty-publication-manager"))
	emptyManager, err := publication.NewManager(publication.Options{Records: emptyPublicationRecords{}, Store: publication.NewMemoryStore(), Clock: clock.Real{}, IDs: id.Generator{}, BaseURL: "https://empty.example.com", PublisherDomain: "empty.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:], Host: "empty.example.com", Audience: publication.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	emptyCoordinator := &publicationWithdrawalCoordinator{managers: map[string]publicationBinding{"empty.example.com": {tenantID: tenantOne, manager: emptyManager}}}
	if hosts, err := emptyCoordinator.WithdrawPublicationDomain(t.Context(), tenantOne, "catalog.example.com", "publisher.publication_domain_revoked", "publisher-one", "publisher_domain_revoked"); err != nil || len(hosts) != 0 {
		t.Fatalf("empty domain withdrawal = %#v, %v", hosts, err)
	}
	if hosts, err := emptyCoordinator.WithdrawPublicationRelease(t.Context(), tenantOne, "release-1", "listing_release.retired", "release-1", "release_retired"); err != nil || len(hosts) != 0 {
		t.Fatalf("empty release withdrawal = %#v, %v", hosts, err)
	}
}

func unsetEnvironment(t *testing.T, name string) {
	t.Helper()
	value, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
