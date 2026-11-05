package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

// These commands must reject invalid input or an in-memory profile before any
// database open. No service composition or database connection is needed.
func TestOperatorCommandsRejectLocalConfigurationBeforeConnecting(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	memory := writeCatalogctlTestConfig(t, config.Defaults())
	missing := filepath.Join(t.TempDir(), "missing.json")
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"probe dispatch", []string{"probe"}, "usage: catalogctl probe"},
		{"smoke dispatch", []string{"smoke", "--unknown"}, "flag provided but not defined"},
		{"seed dispatch", []string{"seed-demo", "--unknown"}, "flag provided but not defined"},
		{"config flag", []string{"config", "validate", "--unknown"}, "flag provided but not defined"},
		{"migration config", []string{"migrate", "plan", "--config", missing}, "no such file"},
		{"restore dispatch", []string{"restore"}, "usage: catalogctl restore verify"},
		{"realm dispatch", []string{"identity", "realms"}, "usage: catalogctl identity realms"},
		{"identity driver", []string{"identity", "provision", "--config", memory}, "database.driver=postgres"},
		{"identity config", []string{"identity", "provision", "--config", missing}, "no such file"},
		{"restore driver", []string{"restore", "verify", "--tenant", tenant, "--recovery-point", "backup", "--config", memory}, "database.driver=postgres"},
		{"realm driver", []string{"identity", "realms", "list", "--tenant", tenant, "--config", memory}, "database.driver=postgres"},
		{"repair driver", []string{"repair", "idempotency", "list", "--tenant", tenant, "--config", memory}, "database.driver=postgres"},
		{"projection driver", []string{"repair", "search-projections", "--tenant", tenant, "--reason", "rebuild", "--config", memory}, "database.driver=postgres"},
		{"restore config", []string{"restore", "verify", "--tenant", tenant, "--recovery-point", "backup", "--config", missing}, "no such file"},
		{"realm config", []string{"identity", "realms", "list", "--tenant", tenant, "--config", missing}, "no such file"},
		{"repair config", []string{"repair", "idempotency", "list", "--tenant", tenant, "--config", missing}, "no such file"},
		{"projection config", []string{"repair", "search-projections", "--tenant", tenant, "--reason", "rebuild", "--config", missing}, "no such file"},
		{"restore flag", []string{"restore", "verify", "--limit", "not-an-int"}, "invalid value"},
		{"realm flag", []string{"identity", "realms", "list", "--limit", "not-an-int"}, "invalid value"},
		{"repair flag", []string{"repair", "idempotency", "list", "--limit", "not-an-int"}, "invalid value"},
		{"projection flag", []string{"repair", "search-projections", "--limit", "not-an-int"}, "invalid value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got error
			output := captureCatalogctlStdout(t, func() error { got = runArgs(test.args); return nil })
			if got == nil || !strings.Contains(got.Error(), test.want) || len(output) != 0 {
				t.Fatalf("command error = %v, output = %q; want %q and no output", got, output, test.want)
			}
		})
	}
}

func TestAuditCommandsRejectUnusableEvidenceLocally(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	export, raw, err := audit.BuildExport("00000000-0000-7000-8000-000000000001", audit.Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := audit.BindTrustedManifest(export, "backup")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for name, value := range map[string][]byte{"export": raw, "manifest": trusted, "invalid": []byte(`{"broken":`)} {
		if err := os.WriteFile(filepath.Join(directory, name), value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory := writeCatalogctlTestConfig(t, config.Defaults())
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"bad flag", []string{"verify-export", "--unknown"}, "flag provided but not defined"},
		{"missing file", []string{"verify-export", "--file", filepath.Join(directory, "missing")}, "no such file"},
		{"invalid export", []string{"verify-export", "--file", filepath.Join(directory, "invalid")}, "validate audit export JSON"},
		{"invalid recovery binding", []string{"bind-manifest", "--file", filepath.Join(directory, "export"), "--recovery-point", "bad\npoint"}, "binding is invalid"},
		{"wrong recovery point", []string{"compare-restore", "--file", filepath.Join(directory, "manifest"), "--recovery-point", "other"}, "binding is invalid"},
		{"invalid manifest", []string{"compare-restore", "--file", filepath.Join(directory, "invalid"), "--recovery-point", "backup"}, "validate trusted audit manifest JSON"},
		{"missing config", []string{"compare-restore", "--file", filepath.Join(directory, "manifest"), "--recovery-point", "backup", "--config", filepath.Join(directory, "missing")}, "no such file"},
		{"memory driver", []string{"compare-restore", "--file", filepath.Join(directory, "manifest"), "--recovery-point", "backup", "--config", memory}, "database.driver=postgres"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got error
			output := captureCatalogctlStdout(t, func() error { got = manageAudit(test.args); return nil })
			if got == nil || !strings.Contains(got.Error(), test.want) || len(output) != 0 {
				t.Fatalf("invalid evidence returned error %v and output %q; want %q and no output", got, output, test.want)
			}
		})
	}
}

func TestBoundedFileExactLimitAndReadFailures(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "evidence")
	want := []byte("12345678")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readBoundedFile(path, len(want)); err != nil || !bytes.Equal(raw, want) {
		t.Fatalf("exact-limit read = %q, %v", raw, err)
	}
	if _, err := readBoundedFile(path+"missing", 8); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open error lost cause: %v", err)
	}
	if raw, err := readBoundedFile(directory, 8); err == nil || !strings.Contains(err.Error(), "read ") || raw != nil {
		t.Fatalf("directory read = %q, %v", raw, err)
	}
}

func TestBoundedJSONOutputPreservesLimitsAndFailures(t *testing.T) {
	want := "{\n  \"value\": \"test\"\n}\n"
	output := captureCatalogctlStdout(t, func() error { return printBoundedJSON(map[string]string{"value": "test"}, len(want)) })
	if string(output) != want {
		t.Fatalf("formatted output = %q, want %q", output, want)
	}
	for _, limit := range []int{0, -1, len(want) - 1} {
		var got error
		output = captureCatalogctlStdout(t, func() error { got = printBoundedJSON(map[string]string{"value": "test"}, limit); return nil })
		if got == nil || len(output) != 0 {
			t.Fatalf("limit %d: error %v, partial output %q", limit, got, output)
		}
	}
	if err := printBoundedJSON(make(chan int), 100); err == nil {
		t.Fatal("unsupported JSON value accepted")
	}
	closed, err := os.CreateTemp(t.TempDir(), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = closed
	defer func() { os.Stdout = original }()
	if err := printBoundedJSON(map[string]string{"value": "test"}, len(want)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("output error lost cause: %v", err)
	}
}

// TestIdentityPlansRejectInvalidBindings rejects malformed authority inputs in
// the planner; no operator command or PostgreSQL connection is constructed.
func TestIdentityPlansRejectInvalidBindings(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, Environment: "test", Profile: "plan-rejection", ServiceName: "catalogctl-test", Database: config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod}}
	path := writeCatalogctlTestConfig(t, cfg)
	base := []string{"provision", "--config", path, "--tenant", tenant, "--tenant-name", "Fixture tenant", "--issuer", "https://issuer.example", "--subject", "admin", "--operator", "operator", "--reason", "Initial administrator"}
	for _, tc := range []struct {
		name, flag, value, want string
	}{
		{"tenant", "--tenant", "invalid", "incomplete or invalid"},
		{"actor type", "--actor-type", "robot", "incomplete or invalid"},
		{"operator", "--operator", " operator ", "incomplete or invalid"},
		{"shared organization", "--tenancy-mode", "shared", "do not agree"},
		{"dedicated organization", "--external-organization", "org", "do not agree"},
		{"tenancy mode", "--tenancy-mode", "other", "must be dedicated or shared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string(nil), base...), tc.flag, tc.value)
			plan, err := planIdentityProvisioning(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(plan.inputs) != 0 || plan.development || plan.configuration.Database.Driver != "" {
				t.Fatalf("invalid binding returned a usable plan or wrong error: %v", err)
			}
		})
	}
	cfg.Auth = config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
		"bad": {TenantID: tenant, Subject: "bad\nsubject", Roles: []identity.Role{identity.RoleTenantAdmin}},
	}}
	plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
	if err == nil || !strings.Contains(err.Error(), "incomplete or invalid") || len(plan.inputs) != 0 || plan.development {
		t.Fatalf("invalid development binding returned a usable plan or wrong error: %v", err)
	}
}

// TestIdentityDevelopmentPlanOrdersSameTenantSubjects keeps otherwise equal
// development principals deterministic regardless of map iteration order.
func TestIdentityDevelopmentPlanOrdersSameTenantSubjects(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, Environment: "test", Profile: "plan-order", ServiceName: "catalogctl-test", Database: config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"first-token": {TenantID: tenant, Subject: "zulu", Roles: []identity.Role{identity.RoleReader}},
			"last-token":  {TenantID: tenant, Subject: "alpha", Roles: []identity.Role{identity.RoleReader}},
		}},
	}
	plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
	if err != nil || !plan.development || len(plan.inputs) != 2 || plan.inputs[0].Subject != "alpha" || plan.inputs[1].Subject != "zulu" {
		t.Fatalf("same-tenant subjects were not ordered deterministically: %#v, %v", plan.inputs, err)
	}
}

// The embedded profile report is an offline command; its functional caller
// still verifies command dispatch alongside diagnostics and bootstrap.
func TestProfileCommandEmitsPinnedBundleReport(t *testing.T) {
	output := captureCatalogctlStdout(t, func() error { return runArgs([]string{"profiles", "verify"}) })
	var report schemas.Verification
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "verified" || report.ArtifactCount < 1 || !strings.HasPrefix(report.BundleDigest, "sha256:") || len(report.BundleDigest) != 71 || len(report.Profiles) != len(schemas.Profiles) {
		t.Fatalf("incomplete bundle report: %+v", report)
	}
	for i, profile := range schemas.Profiles {
		if report.Profiles[i] != profile {
			t.Fatalf("profile %d lost pinned provenance", i)
		}
	}
}

func TestConfigurationCommandChecksRuntimeSecretsBeforeOutput(t *testing.T) {
	// Explicit invalid key data exercises command-level secret validation, not
	// configuration parsing or a provider connection.
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "fixture-invalid-key")
	path := localOperatorTestConfig(t)
	var got error
	output := captureCatalogctlStdout(t, func() error {
		got = runArgs([]string{"config", "validate", "--config", path, "--role", "api"})
		return nil
	})
	want := "CATALOG_CURSOR_SIGNING_KEY or CATALOG_CURSOR_SIGNING_KEY_FILE must provide base64 for exactly 32 bytes"
	if got == nil || got.Error() != want || len(output) != 0 {
		t.Fatalf("secret failure=%v output=%q", got, output)
	}
	// The schema-owner role requires no runtime key. It validates the profile
	// without opening or parsing this database endpoint.
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, Environment: "test", Profile: "migration-preflight", ServiceName: "catalogctl-test", Database: config.Database{Driver: "postgres", DSN: "postgres://must-not-connect.invalid/catalog", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod}}
	output = captureCatalogctlStdout(t, func() error {
		return runArgs([]string{"config", "validate", "--config", writeCatalogctlTestConfig(t, cfg), "--role", "migration"})
	})
	var report struct {
		Status string `json:"status"`
		Role   string `json:"role"`
	}
	if err := json.Unmarshal(output, &report); err != nil || report.Status != "valid" || report.Role != "migration" || bytes.Contains(output, []byte("fixture-invalid-key")) || bytes.Contains(output, []byte("must-not-connect.invalid")) {
		t.Fatalf("migration preflight=%s error=%v", output, err)
	}
}
