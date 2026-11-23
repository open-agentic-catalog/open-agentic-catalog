// Shared-tenancy provisioning is a strict operator workflow: every identity
// value is explicit and the schema-owner DSN comes only from the environment.

package main

import (
	"context"
	"os"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const sharedProvisionUsage = "usage: catalogctl identity provision-shared --tenant <uuid> --tenant-name <text> --issuer <url> --subject <text> --external-organization <key> --namespaces <slug,slug> --operator <text> --reason <text> (requires CATALOG_DATABASE_DRIVER=postgres and CATALOG_DATABASE_DSN)"

type sharedProvisioningPlan struct {
	configuration config.Config
	input         database.SharedTenantProvisioningInput
}

// manageSharedProvisioning plans first, so missing input or DSN fails before
// any connection, then applies the single request through owner authority.
func manageSharedProvisioning(arguments []string) error {
	plan, err := planSharedProvisioning(arguments)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, plan.configuration.Database.DSN, databasePoolConfig(plan.configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	result, err := database.ProvisionSharedTenant(ctx, pool, plan.input)
	if err != nil {
		return err
	}
	return printJSON(result)
}

// planSharedProvisioning requires every input and accepts the DSN only from
// CATALOG_DATABASE_DSN: no file variant, no JSON configuration and no default.
func planSharedProvisioning(arguments []string) (sharedProvisioningPlan, error) {
	flags := diagnostic.Flags("catalogctl identity provision-shared")
	tenantID := flags.String("tenant", "", "internal tenant UUID")
	tenantName := flags.String("tenant-name", "", "tenant display name")
	issuer := flags.String("issuer", "", "exact trusted JWT issuer")
	subject := flags.String("subject", "", "initial administrator subject")
	organization := flags.String("external-organization", "", "external organization key from the token claim")
	namespaceList := flags.String("namespaces", "", "comma-separated namespace slugs")
	operator := flags.String("operator", "", "attributable provisioning operator")
	reason := flags.String("reason", "", "provisioning reason retained in audit")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return sharedProvisioningPlan{}, err
	}
	if flags.NArg() != 0 || *tenantID == "" || *tenantName == "" || *issuer == "" || *subject == "" || *organization == "" || *namespaceList == "" || *operator == "" || *reason == "" {
		return sharedProvisioningPlan{}, safeerr.New(sharedProvisionUsage)
	}
	if dsn, present := os.LookupEnv("CATALOG_DATABASE_DSN"); !present || dsn == "" {
		return sharedProvisioningPlan{}, safeerr.New("shared tenant provisioning requires CATALOG_DATABASE_DSN")
	}
	if _, present := os.LookupEnv("CATALOG_DATABASE_DSN_FILE"); present {
		return sharedProvisioningPlan{}, safeerr.New("shared tenant provisioning accepts only CATALOG_DATABASE_DSN, not CATALOG_DATABASE_DSN_FILE")
	}
	configuration, err := config.LoadFor("", config.RoleMigration)
	if err != nil {
		return sharedProvisioningPlan{}, err
	}
	if configuration.Database.Driver != "postgres" {
		return sharedProvisioningPlan{}, safeerr.New("shared tenant provisioning requires CATALOG_DATABASE_DRIVER=postgres")
	}
	namespaces := make([]string, 0)
	for _, slug := range strings.Split(*namespaceList, ",") {
		namespaces = append(namespaces, strings.TrimSpace(slug))
	}
	input := database.SharedTenantProvisioningInput{
		TenantID: *tenantID, TenantDisplayName: *tenantName, Issuer: *issuer, ExternalOrganization: *organization,
		Subject: *subject, Namespaces: namespaces, Operator: *operator, Reason: *reason,
		AcknowledgeOutbox: !configuration.Webhooks.Enabled,
	}
	if err := database.ValidateSharedTenantProvisioningInput(input); err != nil {
		return sharedProvisioningPlan{}, err
	}
	return sharedProvisioningPlan{configuration: configuration, input: input}, nil
}
