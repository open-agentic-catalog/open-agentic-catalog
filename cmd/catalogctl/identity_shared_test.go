package main

import (
	"os"
	"strings"
	"testing"
)

// unsetEnvironment removes a variable that the caller first set with t.Setenv,
// so the test framework restores the original value afterwards.
func unsetEnvironment(name string) error { return os.Unsetenv(name) }

const sharedTestTenant = "00000000-0000-7000-8000-000000000001"

func sharedTestArguments() []string {
	return []string{
		"--tenant", sharedTestTenant,
		"--tenant-name", "Global",
		"--issuer", "https://identity.example.com/realms/shared",
		"--subject", "initial-admin",
		"--external-organization", "organization-1",
		"--namespaces", "alpha, beta",
		"--operator", "release-operator",
		"--reason", "Provision the shared tenant",
	}
}

// withoutFlag returns the arguments without one flag and its value.
func withoutFlag(arguments []string, name string) []string {
	var result []string
	for index := 0; index < len(arguments); index += 2 {
		if arguments[index] != name {
			result = append(result, arguments[index], arguments[index+1])
		}
	}
	return result
}

// sharedTestEnvironment selects a PostgreSQL driver and an unreachable DSN so a
// planner that connected would fail by name resolution, never by success.
func sharedTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CATALOG_DATABASE_DRIVER", "postgres")
	t.Setenv("CATALOG_DATABASE_DSN", "postgres://must-not-connect.invalid/catalog")
	t.Setenv("CATALOG_DATABASE_DSN_FILE", "")
	// A present-but-empty file variable is invalid, so remove it entirely.
	if err := unsetEnvironment("CATALOG_DATABASE_DSN_FILE"); err != nil {
		t.Fatal(err)
	}
}

func TestSharedProvisioningPlanAcceptsCompleteInput(t *testing.T) {
	sharedTestEnvironment(t)
	plan, err := planSharedProvisioning(sharedTestArguments())
	if err != nil {
		t.Fatal(err)
	}
	input := plan.input
	if input.TenantID != sharedTestTenant || input.TenantDisplayName != "Global" || input.ExternalOrganization != "organization-1" || len(input.Namespaces) != 2 || input.Namespaces[0] != "alpha" || input.Namespaces[1] != "beta" {
		t.Fatalf("shared provisioning input = %#v", input)
	}
}

func TestSharedProvisioningPlanFailsClosedBeforeAnyConnection(t *testing.T) {
	sharedTestEnvironment(t)
	if _, err := planSharedProvisioning(nil); err == nil || !strings.Contains(err.Error(), "usage: catalogctl identity provision-shared") {
		t.Fatalf("no arguments returned %v; want the usage error", err)
	}
	for _, name := range []string{"--tenant", "--tenant-name", "--issuer", "--subject", "--external-organization", "--namespaces", "--operator", "--reason"} {
		t.Run("missing "+name, func(t *testing.T) {
			if _, err := planSharedProvisioning(withoutFlag(sharedTestArguments(), name)); err == nil || !strings.Contains(err.Error(), "usage: catalogctl identity provision-shared") {
				t.Fatalf("missing %s returned %v; want the usage error", name, err)
			}
		})
	}
	for name, arguments := range map[string][]string{
		"positional":      append(sharedTestArguments(), "extra"),
		"unknown flag":    append(sharedTestArguments(), "--tenancy-mode", "dedicated"),
		"invalid tenant":  append(withoutFlag(sharedTestArguments(), "--tenant"), "--tenant", "tenant"),
		"invalid slug":    append(withoutFlag(sharedTestArguments(), "--namespaces"), "--namespaces", "Alpha"),
		"empty slug":      append(withoutFlag(sharedTestArguments(), "--namespaces"), "--namespaces", "alpha,,beta"),
		"config flag":     append(sharedTestArguments(), "--config", "catalog.json"),
		"blank operator":  append(withoutFlag(sharedTestArguments(), "--operator"), "--operator", " "),
		"blank reason":    append(withoutFlag(sharedTestArguments(), "--reason"), "--reason", " "),
		"padded operator": append(withoutFlag(sharedTestArguments(), "--operator"), "--operator", " operator"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planSharedProvisioning(arguments); err == nil {
				t.Fatalf("arguments %#v were accepted", arguments)
			}
		})
	}
}

func TestSharedProvisioningRequiresTheDSNEnvironmentVariable(t *testing.T) {
	sharedTestEnvironment(t)
	t.Setenv("CATALOG_DATABASE_DSN", "")
	if _, err := planSharedProvisioning(sharedTestArguments()); err == nil || !strings.Contains(err.Error(), "requires CATALOG_DATABASE_DSN") {
		t.Fatalf("blank DSN returned %v", err)
	}
	if err := unsetEnvironment("CATALOG_DATABASE_DSN"); err != nil {
		t.Fatal(err)
	}
	if _, err := planSharedProvisioning(sharedTestArguments()); err == nil || !strings.Contains(err.Error(), "requires CATALOG_DATABASE_DSN") {
		t.Fatalf("missing DSN returned %v", err)
	}
	// No localhost fallback: the error must not mention a default address.
	if _, err := planSharedProvisioning(sharedTestArguments()); err != nil && strings.Contains(err.Error(), "localhost") {
		t.Fatalf("missing DSN produced a localhost fallback: %v", err)
	}
}

func TestSharedProvisioningRejectsOtherDSNSources(t *testing.T) {
	sharedTestEnvironment(t)
	t.Setenv("CATALOG_DATABASE_DSN_FILE", "/run/secrets/dsn")
	if _, err := planSharedProvisioning(sharedTestArguments()); err == nil {
		t.Fatal("the DSN file variable was accepted next to the DSN variable")
	}
	if err := unsetEnvironment("CATALOG_DATABASE_DSN"); err != nil {
		t.Fatal(err)
	}
	if _, err := planSharedProvisioning(sharedTestArguments()); err == nil {
		t.Fatal("the DSN file variable alone was accepted")
	}
}

func TestSharedProvisioningRequiresPostgreSQL(t *testing.T) {
	sharedTestEnvironment(t)
	t.Setenv("CATALOG_DATABASE_DRIVER", "memory")
	if _, err := planSharedProvisioning(sharedTestArguments()); err == nil {
		t.Fatal("the memory driver was accepted")
	}
}

func TestIdentityDispatchRoutesProvisionShared(t *testing.T) {
	sharedTestEnvironment(t)
	if err := runArgs([]string{"identity", "provision-shared"}); err == nil || !strings.Contains(err.Error(), "provision-shared") {
		t.Fatalf("dispatch without arguments returned %v; want the provision-shared usage error", err)
	}
}
