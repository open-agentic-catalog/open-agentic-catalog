// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestTopLevelHelpVersionAndUnknownCommand(t *testing.T) {
	for _, argument := range []string{"help", "-h", "--help", "version", "--version"} {
		output := captureCatalogctlStdout(t, func() error { return runArgs([]string{argument}) })
		if strings.Contains(argument, "version") {
			var result map[string]string
			if err := json.Unmarshal(output, &result); err != nil || len(result) != 3 || result["version"] != version || result["commit"] != commit || result["sourceState"] != "unknown" {
				t.Fatalf("version output=%s error=%v", output, err)
			}
		} else if !strings.HasPrefix(string(output), "usage: catalogctl <auth|remote|") {
			t.Fatalf("help output=%s", output)
		}
	}
	original := os.Args
	os.Args = []string{"catalogctl", "unknown-from-argv"}
	defer func() { os.Args = original }()
	if err := run(); err == nil || err.Error() != `unknown catalogctl command "unknown-from-argv"` {
		t.Fatalf("process argument dispatch=%v", err)
	}
	if err := runArgs([]string{"unknown"}); err == nil {
		t.Fatal("unknown command succeeded")
	}
	if err := diagnostics([]string{"--role", "combined"}); err == nil {
		t.Fatal("invalid diagnostics role succeeded")
	}
}

func TestDiagnosticWorkLimitsMatchRuntimeEnforcement(t *testing.T) {
	configuration := config.Defaults()
	limits := effectiveWorkLimits(configuration)
	if limits.RequestTimeout != configuration.HTTP.RequestTimeout || limits.PublicationRecords != catalog.MaxPublicationRecords || limits.PublicationProjectionBytes != catalog.MaxPublicationProjectionBytes || limits.PublicationARDEntries != publication.MaxPublicationARDEntries || limits.PublicationAICatalogEntries != publication.MaxPublicationAICatalogEntries || limits.PublicationExclusions != publication.MaxPublicationExclusions || limits.PublicationArtifactReferences != publication.MaxPublicationArtifactReferences || limits.PublicationDocumentBytes != publication.MaxPublicationDocumentBytes || limits.PublicationSnapshotBytes != publication.MaxPublicationSnapshotBytes || limits.PublicationWorkUnits != publication.MaxPublicationWorkUnits || limits.PublicationConcurrentConstruction != publication.MaxConcurrentConstructions || limits.LiveFederationPageSize != federation.MaxLiveSearchPageSize {
		t.Fatalf("diagnostic work limits = %#v", limits)
	}
}

func TestLoopbackHealthProbeInputFailures(t *testing.T) {
	for name, arguments := range map[string][]string{
		"missing URL":        {},
		"unknown flag":       {"--unknown"},
		"malformed status":   {"--expect-status", "invalid"},
		"external host":      {"--url", "http://example.com:8080/livez"},
		"hostname":           {"--url", "http://localhost:8080/livez"},
		"HTTPS":              {"--url", "https://127.0.0.1:8080/livez"},
		"missing port":       {"--url", "http://127.0.0.1/livez"},
		"credentials":        {"--url", "http://user:password@127.0.0.1:8080/livez"},
		"query":              {"--url", "http://127.0.0.1:8080/livez?token=private"},
		"empty query marker": {"--url", "http://127.0.0.1:8080/livez?"},
		"encoded path":       {"--url", "http://127.0.0.1:8080/live%7a"},
		"unsupported path":   {"--url", "http://127.0.0.1:8080" + "/metrics"},
		"unsupported status": {"--url", "http://127.0.0.1:8080" + "/livez", "--expect-status", "201"},
		"extra argument":     {"--url", "http://127.0.0.1:8080" + "/livez", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := probe(arguments); err == nil {
				t.Fatalf("invalid probe arguments %#v succeeded", arguments)
			}
		})
	}
	if err := printBoundedJSON(map[string]string{"value": strings.Repeat("x", 20)}, 10); err == nil {
		t.Fatal("bounded JSON writer accepted an oversized report")
	}
}

func TestAuditExportVerifierCommandsAreBoundedAndNetworkFree(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	export, raw, err := audit.BuildExport("00000000-0000-7000-8000-000000000001", audit.Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := canonicalTempDir(t)
	exportPath := filepath.Join(directory, "audit-export.json")
	if err := os.WriteFile(exportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"audit", "verify-export", "--file", exportPath},
		{"audit", "bind-manifest", "--file", exportPath, "--recovery-point", "backup-set-17"},
	} {
		output := captureCatalogctlStdout(t, func() error { return runArgs(arguments) })
		if arguments[1] == "verify-export" {
			var report audit.VerificationReport
			if err := json.Unmarshal(output, &report); err != nil || !report.Valid || report.Mode != "file" || report.RowCount != 0 || report.EventsDigest != export.Manifest.EventsDigest || report.RecoveryPointID != "" || report.AssuranceBoundary == "" {
				t.Fatalf("verification output = %s, %v", output, err)
			}
		} else {
			trusted, err := audit.ParseTrustedManifest(output, "backup-set-17")
			if err != nil || !audit.ManifestsEqual(trusted.Manifest, export.Manifest) {
				t.Fatalf("binding output did not preserve export manifest: %s, %v", output, err)
			}
		}
	}
	for _, arguments := range [][]string{
		{"audit"},
		{"audit", "unknown", "--file", exportPath},
		{"audit", "verify-export"},
		{"audit", "verify-export", "--file", exportPath, "--recovery-point", "unexpected"},
		{"audit", "bind-manifest", "--file", exportPath},
		{"audit", "bind-manifest", "--file", exportPath, "--recovery-point", "backup", "--config", "unexpected.json"},
		{"audit", "compare-restore", "--file", exportPath, "--recovery-point", "backup", "extra"},
	} {
		if err := runArgs(arguments); err == nil {
			t.Fatalf("invalid audit arguments %v succeeded", arguments)
		}
	}
	emptyPath := filepath.Join(directory, "empty.json")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(emptyPath, 8); err == nil {
		t.Fatal("empty audit verifier input was accepted")
	}
	oversizedPath := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversizedPath, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(oversizedPath, 8); err == nil {
		t.Fatal("oversized audit verifier input was accepted")
	}
}

func TestRestoreVerificationPlanningIsStrictAndNetworkFree(t *testing.T) {
	base := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test",
		Profile:     "restore-plan-test",
		ServiceName: "open-agentic-catalogctl-test",
		Database:    config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
	}
	path := writeCatalogctlTestConfig(t, base)
	arguments := []string{"verify", "--config", path, "--tenant", "00000000-0000-7000-8000-000000000001", "--recovery-point", "backup-set-17", "--api-role", "exact_api", "--worker-role", "exact_worker", "--limit", "7"}
	plan, err := planRestoreVerification(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if plan.configuration.Database.DSN != base.Database.DSN || plan.options.TenantID != "00000000-0000-7000-8000-000000000001" || plan.options.RecoveryPoint != "backup-set-17" || plan.options.APIRole != "exact_api" || plan.options.WorkerRole != "exact_worker" || plan.options.Limit != 7 {
		t.Fatalf("restore verification plan = %#v", plan)
	}
	for _, invalid := range [][]string{
		{},
		{"inspect"},
		{"verify", "--config", path, "--tenant", "bad", "--recovery-point", "backup"},
		{"verify", "--config", path, "--tenant", plan.options.TenantID, "--recovery-point", "line\nbreak"},
		{"verify", "--config", path, "--tenant", plan.options.TenantID, "--recovery-point", "backup", "--limit", "0"},
		{"verify", "--config", path, "--tenant", plan.options.TenantID, "--recovery-point", "backup", "--api-role", "same", "--worker-role", "same"},
		append(append([]string(nil), arguments...), "extra"),
	} {
		if _, err := planRestoreVerification(invalid); err == nil {
			t.Fatalf("invalid restore arguments %#v succeeded", invalid)
		}
	}
	memory := base
	memory.Database = config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod}
	memoryPath := writeCatalogctlTestConfig(t, memory)
	if _, err := planRestoreVerification([]string{"verify", "--config", memoryPath, "--tenant", plan.options.TenantID, "--recovery-point", "backup"}); err == nil {
		t.Fatal("memory restore verification plan succeeded")
	}
	if database.DefaultRestoreVerificationLimit < 1 {
		t.Fatal("restore default limit is invalid")
	}
}

func TestLocalOperatorArgumentFailures(t *testing.T) {
	path := localOperatorTestConfig(t)
	for name, arguments := range map[string][]string{
		"missing command":  nil,
		"migrate action":   {"migrate"},
		"config action":    {"config"},
		"config role":      {"config", "validate", "--config", path, "--role", "invalid"},
		"migrate flag":     {"migrate", "plan", "--unknown"},
		"migrate argument": {"migrate", "plan", "extra"},
		"migrate roles":    {"migrate", "apply", "--api-role", "catalog-runtime", "--worker-role", "catalog-runtime"},
		"migrate driver":   {"migrate", "plan", "--config", path},
		"profiles action":  {"profiles", "list"},
		"diagnostics flag": {"diagnostics", "--unknown"},
		"bootstrap flag":   {"bootstrap", "--unknown"},
	} {
		if err := runArgs(arguments); err == nil {
			t.Fatalf("%s arguments %v succeeded", name, arguments)
		}
	}
}

func localOperatorTestConfig(t *testing.T) string {
	t.Helper()
	tenantID := "00000000-0000-7000-8000-000000000001"
	path := writeCatalogctlTestConfig(t, config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "development",
		Profile:     "catalogctl-test",
		ServiceName: "open-agentic-catalogctl-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: testDuration(time.Second), RequestTimeout: testDuration(time.Second), WriteTimeout: testDuration(7 * time.Second), IdleTimeout: testDuration(time.Second), ShutdownTimeout: testDuration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin": {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: testDuration(time.Hour)},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: testDuration(time.Millisecond), LeaseDuration: testDuration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: testDuration(15 * time.Minute), HousekeepingInterval: testDuration(5 * time.Minute), TerminalJobRetention: testDuration(168 * time.Hour), AcknowledgedOutboxRetention: testDuration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: testDuration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   config.Discovery{Hosts: []config.DiscoveryHost{{Host: "127.0.0.1", TenantID: tenantID, Mode: "anonymous", PublisherDomain: "catalog.example.com"}}},
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	})

	return path
}

func TestFirstAdministratorTokenIsDeterministicAndRequiresRole(t *testing.T) {
	tenantID := "00000000-0000-7000-8000-000000000001"
	configuration := config.Config{Auth: config.Auth{DevelopmentTokens: map[string]config.DevelopmentUser{
		"z-admin": {Subject: "later", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		"a-admin": {Subject: "first", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		"reader":  {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
	}}}
	if token := firstAdministratorToken(configuration); token != "a-admin" {
		t.Fatalf("first administrator token = %q", token)
	}
	configuration.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"reader": {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}}}
	if token := firstAdministratorToken(configuration); token != "" {
		t.Fatalf("reader selected as administrator: %q", token)
	}
}

func TestStaticConfigurationValidationRejectsPostgresCombinedRoleWithoutConnecting(t *testing.T) {
	tenantID := "00000000-0000-7000-8000-000000000001"
	path := writeCatalogctlTestConfig(t, config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test",
		Profile:     "static-validation-test",
		ServiceName: "open-agentic-catalogctl-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: testDuration(time.Second), RequestTimeout: testDuration(time.Second), WriteTimeout: testDuration(7 * time.Second), IdleTimeout: testDuration(time.Second), ShutdownTimeout: testDuration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin": {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: testDuration(time.Hour)},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: testDuration(time.Millisecond), LeaseDuration: testDuration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: testDuration(15 * time.Minute), HousekeepingInterval: testDuration(5 * time.Minute), TerminalJobRetention: testDuration(168 * time.Hour), AcknowledgedOutboxRetention: testDuration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: testDuration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   config.Discovery{Hosts: []config.DiscoveryHost{{Host: "127.0.0.1", TenantID: tenantID, Mode: "anonymous", PublisherDomain: "catalog.example.com"}}},
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	})

	if err := runArgs([]string{"config", "validate", "--config", path, "--role", "api"}); err != nil {
		t.Fatalf("static API validation opened a dependency or failed valid configuration: %v", err)
	}
	if err := runArgs([]string{"config", "validate", "--config", path, "--role", "all"}); err == nil {
		t.Fatal("static validation accepted PostgreSQL for the combined role")
	}
	if err := runArgs([]string{"config", "validate", "--config", path, "extra"}); err == nil {
		t.Fatal("static validation accepted a positional argument")
	}
}

func testDuration(value time.Duration) config.Duration {
	return config.Duration{Duration: value}
}

func TestRepairCommandRequiresReviewedBoundedArgumentsBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"repair"},
		{"repair", "unknown", "plan"},
	} {
		if err := runArgs(arguments); err == nil || !strings.Contains(err.Error(), "usage: catalogctl repair") {
			t.Fatalf("arguments %#v returned %v", arguments, err)
		}
	}
	for _, arguments := range [][]string{
		{"repair", "idempotency", "unknown"},
		{"repair", "idempotency", "plan"},
		{"repair", "idempotency", "plan", "--tenant", "tenant", "--reservation", "reservation", "--reason", "reason", "--plan-digest", "unexpected"},
		{"repair", "idempotency", "plan", "--tenant", "tenant", "--reservation", "reservation", "--reason", "reason", "--limit", "1"},
		{"repair", "idempotency", "apply", "--tenant", "tenant", "--reservation", "reservation", "--reason", "reason"},
		{"repair", "idempotency", "apply", "--tenant", "tenant", "--reservation", "reservation", "--reason", "reason", "--plan-digest", "digest", "--limit", "1"},
		{"repair", "idempotency", "apply", "--tenant", "tenant", "--reservation", "reservation", "--reason", "reason", "--plan-digest", "digest", "extra"},
	} {
		if err := runArgs(arguments); err == nil || !strings.Contains(err.Error(), "usage: catalogctl repair idempotency") {
			t.Fatalf("arguments %#v returned %v", arguments, err)
		}
	}
	for _, arguments := range [][]string{
		{"repair", "search-projections"},
		{"repair", "search-projections", "--tenant", "tenant"},
		{"repair", "search-projections", "--tenant", "00000000-0000-7000-8000-000000000001", "--limit", "0"},
		{"repair", "search-projections", "--tenant", "00000000-0000-7000-8000-000000000001", "extra"},
	} {
		if err := runArgs(arguments); err == nil || !strings.Contains(err.Error(), "usage: catalogctl repair search-projections") {
			t.Fatalf("arguments %#v returned %v", arguments, err)
		}
	}
}

func TestIdentityProvisioningPlanningIsDeterministicAndNetworkFree(t *testing.T) {
	const tenantOne = "00000000-0000-7000-8000-000000000001"
	const tenantTwo = "00000000-0000-7000-8000-000000000002"
	base := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test",
		Profile:     "identity-plan-test",
		ServiceName: "open-agentic-catalogctl-test",
		Database:    config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
	}
	manualPath := writeCatalogctlTestConfig(t, base)
	manual, err := planIdentityProvisioning([]string{
		"provision", "--config", manualPath,
		"--tenant", tenantOne,
		"--tenant-name", "Example tenant",
		"--issuer", "https://identity.example.com",
		"--subject", "initial-admin",
		"--roles", "reader, tenant_admin,reader",
		"--operator", "release-operator",
		"--reason", "Initial tenant bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if manual.development || len(manual.inputs) != 1 || manual.inputs[0].TenantID != tenantOne || len(manual.inputs[0].Roles) != 2 || manual.inputs[0].Roles[0] != identity.RoleReader || manual.inputs[0].Roles[1] != identity.RoleTenantAdmin {
		t.Fatalf("manual provisioning plan = %#v", manual)
	}

	development := base
	development.Auth = config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
		"reader":  {TenantID: tenantTwo, Subject: "reader", Roles: []identity.Role{identity.RoleReader}},
		"service": {TenantID: tenantOne, Subject: "service", Roles: []identity.Role{identity.RoleServiceClient}},
		"admin":   {TenantID: tenantTwo, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}},
	}}
	developmentPath := writeCatalogctlTestConfig(t, development)
	planned, err := planIdentityProvisioning([]string{"provision-development", "--config", developmentPath})
	if err != nil {
		t.Fatal(err)
	}
	if !planned.development || len(planned.inputs) != 3 {
		t.Fatalf("development provisioning plan = %#v", planned)
	}
	if planned.inputs[0].Subject != "admin" || planned.inputs[1].Subject != "service" || planned.inputs[2].Subject != "reader" {
		t.Fatalf("development provisioning order = %q, %q, %q", planned.inputs[0].Subject, planned.inputs[1].Subject, planned.inputs[2].Subject)
	}
	if planned.inputs[0].ActorType != identity.ActorHuman || planned.inputs[1].ActorType != identity.ActorService || planned.inputs[2].ActorType != identity.ActorHuman {
		t.Fatalf("development actor types = %q, %q, %q", planned.inputs[0].ActorType, planned.inputs[1].ActorType, planned.inputs[2].ActorType)
	}
	for _, input := range planned.inputs {
		if input.Issuer != "urn:open-agentic-catalog:development" || input.TenancyMode != "dedicated" || input.Operator != "catalogctl-development-bootstrap" {
			t.Fatalf("development provisioning input = %#v", input)
		}
	}

	development.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{
		"exact-service": {TenantID: tenantOne, Subject: "service", RoleTemplates: []string{"service_client"}},
		"exact-admin":   {TenantID: tenantTwo, Subject: "admin", RoleTemplates: []string{"tenant_admin"}},
	}
	exactPlan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, development)})
	if err != nil || len(exactPlan.inputs) != 2 || exactPlan.inputs[0].Subject != "admin" || exactPlan.inputs[1].ActorType != identity.ActorService || len(exactPlan.inputs[0].Roles) != 0 || len(exactPlan.inputs[0].RoleTemplates) != 1 || firstAdministratorToken(development) != "exact-admin" {
		t.Fatalf("exact template plan %#v, %v", exactPlan.inputs, err)
	}

	if roles, parseErr := parseIdentityRoles("tenant_admin,reader,tenant_admin"); parseErr != nil || len(roles) != 2 {
		t.Fatalf("deduplicated roles = %#v, %v", roles, parseErr)
	}
	for _, value := range []string{"", "reader,owner"} {
		if _, parseErr := parseIdentityRoles(value); parseErr == nil {
			t.Fatalf("invalid role list %q was accepted", value)
		}
	}

	production := development
	production.Environment = "production"
	productionPath := writeCatalogctlTestConfig(t, production)
	oidc := development
	oidc.Auth.Mode = "oidc"
	oidcPath := writeCatalogctlTestConfig(t, oidc)
	for name, arguments := range map[string][]string{
		"missing action":       nil,
		"unknown action":       {"list"},
		"unknown flag":         {"provision", "--unknown"},
		"positional":           {"provision", "--config", manualPath, "extra"},
		"missing required":     {"provision", "--config", manualPath},
		"invalid role":         {"provision", "--config", manualPath, "--roles", "owner"},
		"production fixture":   {"provision-development", "--config", productionPath},
		"non-development auth": {"provision-development", "--config", oidcPath},
	} {
		t.Run(name, func(t *testing.T) {
			if _, planErr := planIdentityProvisioning(arguments); planErr == nil {
				t.Fatalf("arguments %#v were accepted", arguments)
			}
		})
	}
	if err := runArgs([]string{"identity", "list"}); err == nil {
		t.Fatal("unknown identity action succeeded")
	}
}

func TestIdentityRealmAdministrationPlanningIsBoundedAndNetworkFree(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const realmID = "00000000-0000-7000-8000-000000000010"
	configuration := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test",
		Profile:     "identity-realm-plan-test",
		ServiceName: "open-agentic-catalogctl-test",
		Database:    config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Discovery:   config.Discovery{Hosts: []config.DiscoveryHost{{Host: "catalog.example.com", TenantID: tenantID, Mode: "disabled", PublisherDomain: "catalog.example.com"}}},
	}
	path := writeCatalogctlTestConfig(t, configuration)
	list, err := planIdentityRealmCommand([]string{"list", "--config", path, "--tenant", tenantID, "--limit", "25", "--after", realmID})
	if err != nil || list.action != "list" || list.limit != 25 || list.after != realmID {
		t.Fatalf("realm list plan = %#v, %v", list, err)
	}
	suspend, err := planIdentityRealmCommand([]string{"suspend", "--config", path, "--tenant", tenantID, "--realm", realmID, "--revision", "3", "--operator", "release-operator", "--reason", "Tenant access shutdown"})
	if err != nil || suspend.action != "suspend" || suspend.revision != 3 || suspend.operator != "release-operator" || !tenantDiscoveryDisabled(suspend.configuration, tenantID) {
		t.Fatalf("realm suspension plan = %#v, %v", suspend, err)
	}
	configuration.Discovery.Hosts[0].Mode = "authenticated"
	if tenantDiscoveryDisabled(configuration, tenantID) {
		t.Fatal("active tenant discovery host was reported disabled")
	}
	if !tenantDiscoveryDisabled(configuration, "00000000-0000-7000-8000-000000000002") {
		t.Fatal("tenant without a configured discovery host was reported exposed")
	}
	for _, arguments := range [][]string{
		nil,
		{"unknown", "--tenant", tenantID},
		{"list", "--config", path, "--tenant", tenantID, "--limit", "0"},
		{"list", "--config", path, "--tenant", tenantID, "--realm", realmID},
		{"suspend", "--config", path, "--tenant", tenantID, "--realm", realmID, "--revision", "1", "--operator", "operator"},
		{"reactivate", "--config", path, "--tenant", tenantID, "--realm", realmID, "--revision", "1", "--operator", "operator", "--reason", "reason", "--limit", "1"},
	} {
		if _, err := planIdentityRealmCommand(arguments); err == nil {
			t.Fatalf("invalid realm arguments %#v were accepted", arguments)
		}
	}
}

func writeCatalogctlTestConfig(t *testing.T, configuration config.Config) string {
	t.Helper()
	raw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalTempDir(t), "catalogctl.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// canonicalTempDir resolves only a newly allocated, trusted test root. Explicit
// unsafe paths still reach the production symlink-refusal boundary unchanged.
func canonicalTempDir(tb testing.TB) string {
	tb.Helper()
	directory, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	return directory
}
