// Package main implements the catalogctl operator command.
// Remote reads use explicit API inputs. Migration, identity, and repair commands
// use their own operator authority; runtime construction does not start listeners
// or tracing. Development smoke and seed commands require an owned target.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
	_ "golang.org/x/crypto/x509roots/fallback"
)

const maxDiagnosticsBytes = 256 << 10

type diagnosticsWorkLimits struct {
	RequestTimeout                    config.Duration `json:"requestTimeout"`
	PublicationRecords                int             `json:"publicationRecords"`
	PublicationProjectionBytes        int             `json:"publicationProjectionBytes"`
	PublicationARDEntries             int             `json:"publicationARDEntries"`
	PublicationAICatalogEntries       int             `json:"publicationAICatalogEntries"`
	PublicationExclusions             int             `json:"publicationExclusions"`
	PublicationArtifactReferences     int             `json:"publicationArtifactReferences"`
	PublicationDocumentBytes          int             `json:"publicationDocumentBytes"`
	PublicationSnapshotBytes          int             `json:"publicationSnapshotBytes"`
	PublicationWorkUnits              int             `json:"publicationWorkUnits"`
	PublicationConcurrentConstruction int             `json:"publicationConcurrentConstructions"`
	LiveFederationPageSize            int             `json:"liveFederationPageSize"`
}

// effectiveWorkLimits reports configured request time and code-owned construction
// ceilings without loading tenant records or including deployment secrets.
func effectiveWorkLimits(configuration config.Config) diagnosticsWorkLimits {
	return diagnosticsWorkLimits{
		RequestTimeout:                    configuration.HTTP.RequestTimeout,
		PublicationRecords:                catalog.MaxPublicationRecords,
		PublicationProjectionBytes:        catalog.MaxPublicationProjectionBytes,
		PublicationARDEntries:             publication.MaxPublicationARDEntries,
		PublicationAICatalogEntries:       publication.MaxPublicationAICatalogEntries,
		PublicationExclusions:             publication.MaxPublicationExclusions,
		PublicationArtifactReferences:     publication.MaxPublicationArtifactReferences,
		PublicationDocumentBytes:          publication.MaxPublicationDocumentBytes,
		PublicationSnapshotBytes:          publication.MaxPublicationSnapshotBytes,
		PublicationWorkUnits:              publication.MaxPublicationWorkUnits,
		PublicationConcurrentConstruction: publication.MaxConcurrentConstructions,
		LiveFederationPageSize:            federation.MaxLiveSearchPageSize,
	}
}

var (
	version     = "dev"
	commit      = "unknown"
	sourceState = "unknown"
)

// main reports command failures to stderr and returns a nonzero process exit code.
func main() {
	diagnostic.ProtectStandardLog("catalogctl")
	if err := run(); err != nil && (!errors.Is(err, flag.ErrHelp) || errors.Is(err, safeerr.ErrPanic)) {
		reportError(os.Stderr, err)
		os.Exit(1)
	}
}

// run passes process arguments to the testable top-level dispatcher.
func run() (err error) {
	defer safeerr.Recover(&err)
	return runArgs(os.Args[1:])
}

// runArgs selects an explicit operator workflow without constructing runtime authority for help or version output.
func runArgs(arguments []string) error {
	if len(arguments) < 1 {
		return safeerr.New("usage: catalogctl <auth|remote|mcp|import|export|audit|trust|config|migrate|identity|repair|restore|profiles|diagnostics|probe|bootstrap|smoke|seed-demo|version> [arguments]")
	}
	switch arguments[0] {
	case "auth":
		return safeerr.At("remote", authCommand(arguments[1:]))
	case "help", "-h", "--help":
		_, err := fmt.Fprintln(os.Stdout, "usage: catalogctl <auth|remote|mcp|import|export|audit|trust|config|migrate|identity|repair|restore|profiles|diagnostics|probe|bootstrap|smoke|seed-demo|version> [arguments]")
		return err
	case "version", "--version":
		return printJSON(buildinfo.New(version, commit, sourceState))
	case "remote":
		return safeerr.At("remote", remote(arguments[1:]))
	case "mcp":
		return safeerr.At("mcp", mcpCommand(arguments[1:]))
	case "import":
		return safeerr.At("import", importCommand(arguments[1:]))
	case "export":
		return safeerr.At("export", exportCommand(arguments[1:]))
	case "migrate":
		return safeerr.At("migrate", migrate(arguments[1:]))
	case "identity":
		return safeerr.At("identity", manageIdentity(arguments[1:]))
	case "repair":
		return safeerr.At("repair", repair(arguments[1:]))
	case "restore":
		return safeerr.At("restore", restore(arguments[1:]))
	case "audit":
		return safeerr.At("audit", manageAudit(arguments[1:]))
	case "trust":
		return safeerr.At("trust", verifyTrust(arguments[1:]))
	case "config":
		return safeerr.At("config", validateConfiguration(arguments[1:]))
	case "profiles":
		return safeerr.At("profiles", profiles(arguments[1:]))
	case "diagnostics":
		return safeerr.At("diagnostics", diagnostics(arguments[1:]))
	case "probe":
		return safeerr.At("probe", probe(arguments[1:]))
	case "bootstrap":
		return safeerr.At("bootstrap", bootstrap(arguments[1:]))
	case "smoke":
		return safeerr.At("smoke", smoke(arguments[1:]))
	case "seed-demo":
		return safeerr.At("seed-demo", seedDemo(arguments[1:]))
	default:
		return safeerr.Wrap(fmt.Errorf("unknown catalogctl command %q", arguments[0]), "unknown command; use catalogctl --help")
	}
}

type restoreVerificationPlan struct {
	configuration config.Config
	options       database.RestoreVerificationOptions
}

// restore executes a validated bounded restore-verification page using the configured operator database connection.
func restore(arguments []string) error {
	plan, err := planRestoreVerification(arguments)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), database.RestoreVerificationTimeout)
	defer cancel()
	pool, err := database.Open(ctx, plan.configuration.Database.DSN, databasePoolConfig(plan.configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	report, err := database.VerifyRestoredCatalog(ctx, pool, plan.options)
	if err != nil {
		return err
	}
	return printBoundedJSON(report, database.MaxRestoreVerificationOutputBytes)
}

// planRestoreVerification rejects invalid scope, paging and role inputs before opening a PostgreSQL connection.
func planRestoreVerification(arguments []string) (restoreVerificationPlan, error) {
	const usage = "usage: catalogctl restore verify --tenant <uuid> --recovery-point <label> [--api-role <database-role> --worker-role <database-role>] [--cursor <value>] [--limit <1-1000>] [--config <path>]"
	if len(arguments) == 0 || arguments[0] != "verify" {
		return restoreVerificationPlan{}, safeerr.New(usage)
	}
	flags := diagnostic.Flags("catalogctl restore verify")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	tenantID := flags.String("tenant", "", "tenant UUID from the separately retained backup inventory")
	recoveryPoint := flags.String("recovery-point", "", "bounded non-secret recovery-point correlation label")
	apiRole := flags.String("api-role", "catalog_api", "PostgreSQL role used by the Catalog API")
	workerRole := flags.String("worker-role", "catalog_worker", "PostgreSQL role used by Catalog workers")
	cursor := flags.String("cursor", "", "continuation from the preceding gap-free verification page")
	limit := flags.Int("limit", database.DefaultRestoreVerificationLimit, "maximum retained rows checked in this invocation")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return restoreVerificationPlan{}, err
	}
	options := database.RestoreVerificationOptions{TenantID: *tenantID, RecoveryPoint: *recoveryPoint, APIRole: *apiRole, WorkerRole: *workerRole, Cursor: *cursor, Limit: *limit}
	if flags.NArg() != 0 || database.ValidateRestoreVerificationOptions(options) != nil {
		return restoreVerificationPlan{}, safeerr.New(usage)
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return restoreVerificationPlan{}, err
	}
	if configuration.Database.Driver != "postgres" {
		return restoreVerificationPlan{}, safeerr.New("restore verification requires database.driver=postgres")
	}
	return restoreVerificationPlan{configuration: configuration, options: options}, nil
}

// manageAudit verifies or binds local evidence offline, or compares a trusted manifest against restored PostgreSQL state.
func manageAudit(arguments []string) error {
	const usage = "usage: catalogctl audit <verify-export|bind-manifest|compare-restore> --file <path> [--recovery-point <opaque-id>] [--config <path>]"
	if len(arguments) == 0 || arguments[0] != "verify-export" && arguments[0] != "bind-manifest" && arguments[0] != "compare-restore" {
		return safeerr.New(usage)
	}
	action := arguments[0]
	flags := diagnostic.Flags("catalogctl audit " + action)
	filePath := flags.String("file", "", "bounded export or trusted-manifest JSON file")
	recoveryPointID := flags.String("recovery-point", "", "external recovery-point identifier for one quiesced-writer interval")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return err
	}
	configSet := false
	flags.Visit(func(value *flag.Flag) { configSet = configSet || value.Name == "config" })
	if flags.NArg() != 0 || *filePath == "" || action == "verify-export" && (*recoveryPointID != "" || configSet) || action != "verify-export" && *recoveryPointID == "" || action == "bind-manifest" && configSet {
		return safeerr.New(usage)
	}
	raw, err := readBoundedFile(*filePath, audit.MaxExportFileBytes)
	if err != nil {
		return err
	}
	if action == "verify-export" || action == "bind-manifest" {
		export, report, err := audit.VerifyExport(raw)
		if err != nil {
			return err
		}
		if action == "verify-export" {
			return printJSON(report)
		}
		trusted, err := audit.BindTrustedManifest(export, *recoveryPointID)
		if err != nil {
			return err
		}
		return printJSON(trusted)
	}
	trusted, err := audit.ParseTrustedManifest(raw, *recoveryPointID)
	if err != nil {
		return err
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return err
	}
	if configuration.Database.Driver != "postgres" {
		return safeerr.New("restored audit comparison requires database.driver=postgres")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, configuration.Database.DSN, databasePoolConfig(configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	report, err := database.CompareRestoredAudit(ctx, pool, trusted, *recoveryPointID)
	if err != nil {
		return err
	}
	return printJSON(report)
}

// readBoundedFile reads a nonempty evidence file within its byte ceiling and reports read/close failures.
func readBoundedFile(path string, maximum int) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304,G703 -- catalogctl intentionally reads an operator-selected, size-bounded evidence file and never executes or rewrites its contents.
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s: %w", path, closeErr)
	}
	if len(raw) == 0 || len(raw) > maximum {
		return nil, fmt.Errorf("%s is empty or exceeds the %d-byte limit", path, maximum)
	}
	return raw, nil
}

// manageIdentity validates provisioning first, then applies identities in the planned order through operator authority.
func manageIdentity(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "realms" {
		return manageIdentityRealms(arguments[1:])
	}
	plan, err := planIdentityProvisioning(arguments)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, plan.configuration.Database.DSN, databasePoolConfig(plan.configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	results := make([]database.IdentityProvisioningResult, 0, len(plan.inputs))
	for _, input := range plan.inputs {
		result, provisionErr := database.ProvisionIdentity(ctx, pool, input)
		if provisionErr != nil {
			return provisionErr
		}
		results = append(results, result)
	}
	if plan.development {
		return printJSON(map[string]any{"provisioned": results, "total": len(results)})
	}
	return printJSON(results[0])
}

type identityRealmPlan struct {
	configuration config.Config
	action        string
	tenantID      string
	realmID       string
	after         string
	limit         int
	revision      int64
	operator      string
	reason        string
}

// manageIdentityRealms runs a validated realm page or attributed lifecycle transition with deployment discovery state.
func manageIdentityRealms(arguments []string) error {
	plan, err := planIdentityRealmCommand(arguments)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, plan.configuration.Database.DSN, databasePoolConfig(plan.configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	if plan.action == "list" {
		page, err := database.ListIdentityRealms(ctx, pool, plan.tenantID, plan.after, plan.limit)
		if err != nil {
			return err
		}
		return printJSON(page)
	}
	target := identity.RealmSuspended
	if plan.action == "reactivate" {
		target = identity.RealmActive
	}
	result, err := database.TransitionIdentityRealm(ctx, pool, database.IdentityRealmTransitionInput{
		TenantID:                plan.tenantID,
		RealmID:                 plan.realmID,
		ExpectedRevision:        plan.revision,
		TargetState:             target,
		Operator:                plan.operator,
		Reason:                  plan.reason,
		TenantDiscoveryDisabled: tenantDiscoveryDisabled(plan.configuration, plan.tenantID),
		AcknowledgeOutbox:       !plan.configuration.Webhooks.Enabled,
	})
	if err != nil {
		return err
	}
	return printJSON(result)
}

// planIdentityRealmCommand keeps list inputs distinct from reason/revision-bound transitions before database access.
func planIdentityRealmCommand(arguments []string) (identityRealmPlan, error) {
	const usage = "usage: catalogctl identity realms <list|suspend|reactivate> --tenant <uuid> [--after <uuid> --limit <1-100>] [--realm <uuid> --revision <n> --operator <text> --reason <text>] [--config <path>]"
	if len(arguments) == 0 || arguments[0] != "list" && arguments[0] != "suspend" && arguments[0] != "reactivate" {
		return identityRealmPlan{}, safeerr.New(usage)
	}
	action := arguments[0]
	flags := diagnostic.Flags("catalogctl identity realms " + action)
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	tenantID := flags.String("tenant", "", "tenant UUID")
	realmID := flags.String("realm", "", "identity realm UUID")
	after := flags.String("after", "", "last realm UUID from the prior page")
	limit := flags.Int("limit", 50, "maximum realms returned")
	revision := flags.Int64("revision", 0, "current identity realm revision")
	operator := flags.String("operator", "", "attributable operator")
	reason := flags.String("reason", "", "reason retained in audit")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return identityRealmPlan{}, err
	}
	limitSet := false
	afterSet := false
	flags.Visit(func(value *flag.Flag) {
		limitSet = limitSet || value.Name == "limit"
		afterSet = afterSet || value.Name == "after"
	})
	invalidList := action == "list" && (*realmID != "" || *revision != 0 || *operator != "" || *reason != "")
	invalidTransition := action != "list" && (*realmID == "" || *revision <= 0 || *operator == "" || *reason == "" || limitSet || afterSet)
	if flags.NArg() != 0 || !identity.ValidTenantID(*tenantID) || invalidList || invalidTransition || *limit < 1 || *limit > 100 || *after != "" && !identity.ValidTenantID(*after) {
		return identityRealmPlan{}, safeerr.New(usage)
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return identityRealmPlan{}, err
	}
	if configuration.Database.Driver != "postgres" {
		return identityRealmPlan{}, safeerr.New("identity realm administration requires database.driver=postgres")
	}
	return identityRealmPlan{configuration: configuration, action: action, tenantID: *tenantID, realmID: *realmID, after: *after, limit: *limit, revision: *revision, operator: *operator, reason: *reason}, nil
}

// tenantDiscoveryDisabled checks whether every configured discovery host for the selected tenant is disabled.
func tenantDiscoveryDisabled(configuration config.Config, tenantID string) bool {
	for _, host := range configuration.Discovery.Hosts {
		if host.TenantID == tenantID && host.Mode != "disabled" {
			return false
		}
	}
	return true
}

type identityProvisioningPlan struct {
	configuration config.Config
	inputs        []database.IdentityProvisioningInput
	development   bool
}

// planIdentityProvisioning validates explicit production inputs or creates a deterministic administrator-first development plan.
func planIdentityProvisioning(arguments []string) (identityProvisioningPlan, error) {
	if len(arguments) == 0 || arguments[0] != "provision" && arguments[0] != "provision-development" {
		return identityProvisioningPlan{}, safeerr.New("usage: catalogctl identity <provision|provision-development|realms> [arguments]")
	}
	action := arguments[0]
	flags := diagnostic.Flags("catalogctl identity " + action)
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	tenantID := flags.String("tenant", "", "internal tenant UUID")
	tenantName := flags.String("tenant-name", "", "tenant display name")
	issuer := flags.String("issuer", "", "exact trusted JWT issuer")
	tenancyMode := flags.String("tenancy-mode", "dedicated", "dedicated or shared")
	externalOrganization := flags.String("external-organization", "", "shared-mode external organization key")
	subject := flags.String("subject", "", "initial administrator subject")
	actorType := flags.String("actor-type", string(identity.ActorHuman), "human or service")
	roleList := flags.String("roles", string(identity.RoleTenantAdmin), "comma-separated global Catalog roles")
	operator := flags.String("operator", "", "attributable provisioning operator")
	reason := flags.String("reason", "", "provisioning reason retained in audit")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return identityProvisioningPlan{}, err
	}
	if flags.NArg() != 0 {
		return identityProvisioningPlan{}, safeerr.New("identity provisioning does not accept positional arguments")
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return identityProvisioningPlan{}, err
	}
	if configuration.Database.Driver != "postgres" {
		return identityProvisioningPlan{}, safeerr.New("identity provisioning requires database.driver=postgres")
	}
	if action == "provision-development" {
		if configuration.Environment == "production" || configuration.Auth.Mode != "development" {
			return identityProvisioningPlan{}, safeerr.New("provision-development is limited to a non-production development-auth profile")
		}
		seeds := make([]identity.Seed, 0, len(configuration.Auth.DevelopmentTokens))
		for _, user := range configuration.Auth.DevelopmentTokens {
			actorType := user.IdentityType()
			seeds = append(seeds, identity.Seed{TenantID: user.TenantID, Issuer: authn.DevelopmentIssuer, Subject: user.Subject, ActorType: actorType, Roles: user.Roles, RoleTemplates: user.SeedRoleTemplates(), Namespaces: user.Namespaces})
		}
		seeds, err = identity.NormalizeDevelopmentSeeds(seeds)
		if err != nil {
			return identityProvisioningPlan{}, fmt.Errorf("identity provisioning input is incomplete or invalid: %w", err)
		}
		sort.Slice(seeds, func(i, j int) bool {
			iAdmin := slices.Contains(seeds[i].Roles, identity.RoleTenantAdmin) || slices.Contains(seeds[i].RoleTemplates, "tenant_admin")
			jAdmin := slices.Contains(seeds[j].Roles, identity.RoleTenantAdmin) || slices.Contains(seeds[j].RoleTemplates, "tenant_admin")
			if iAdmin != jAdmin {
				return iAdmin
			}
			if seeds[i].TenantID != seeds[j].TenantID {
				return seeds[i].TenantID < seeds[j].TenantID
			}
			return seeds[i].Subject < seeds[j].Subject
		})
		inputs := make([]database.IdentityProvisioningInput, 0, len(seeds))
		for _, seed := range seeds {
			inputs = append(inputs, database.IdentityProvisioningInput{TenantID: seed.TenantID, TenantDisplayName: "Tenant " + seed.TenantID, Issuer: seed.Issuer, TenancyMode: "dedicated", Subject: seed.Subject, ActorType: seed.ActorType, Roles: seed.Roles, RoleTemplates: seed.RoleTemplates, Namespaces: seed.Namespaces, Operator: "catalogctl-development-bootstrap", Reason: "Configured development identity", AcknowledgeOutbox: !configuration.Webhooks.Enabled})
		}
		for _, input := range inputs {
			if err := database.ValidateIdentityProvisioningInput(input); err != nil {
				return identityProvisioningPlan{}, err
			}
		}
		return identityProvisioningPlan{configuration: configuration, inputs: inputs, development: true}, nil
	}
	roles, err := parseIdentityRoles(*roleList)
	if err != nil {
		return identityProvisioningPlan{}, err
	}
	if !slices.Contains(roles, identity.RoleTenantAdmin) || *tenantID == "" || *tenantName == "" || *issuer == "" || *subject == "" || *operator == "" || *reason == "" {
		return identityProvisioningPlan{}, safeerr.New("initial identity provision requires tenant, tenant-name, issuer, subject, operator, reason, and tenant_admin in roles")
	}
	input := database.IdentityProvisioningInput{TenantID: *tenantID, TenantDisplayName: *tenantName, Issuer: *issuer, TenancyMode: *tenancyMode, ExternalOrganization: *externalOrganization, Subject: *subject, ActorType: identity.ActorType(*actorType), Roles: roles, Operator: *operator, Reason: *reason, AcknowledgeOutbox: !configuration.Webhooks.Enabled}
	if err := database.ValidateIdentityProvisioningInput(input); err != nil {
		return identityProvisioningPlan{}, err
	}
	return identityProvisioningPlan{configuration: configuration, inputs: []database.IdentityProvisioningInput{input}}, nil
}

// parseIdentityRoles validates comma-separated Catalog roles and deduplicates them in input order.
func parseIdentityRoles(value string) ([]identity.Role, error) {
	parts := strings.Split(value, ",")
	roles := make([]identity.Role, 0, len(parts))
	for _, part := range parts {
		role := identity.Role(strings.TrimSpace(part))
		if !identity.ValidRole(role) {
			return nil, fmt.Errorf("invalid Catalog role %q", part)
		}
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

// repair dispatches narrowly scoped, plan-bound operator repairs; it does not
// expose a generic mutation or SQL escape hatch.
func repair(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "webhook-secrets" {
		return repairWebhookSecrets(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "search-projections" {
		return repairSearchProjections(arguments[1:])
	}
	if len(arguments) == 0 || arguments[0] != "idempotency" {
		return safeerr.New("usage: catalogctl repair <idempotency|search-projections|webhook-secrets> [arguments]")
	}
	if len(arguments) < 2 || arguments[1] != "list" && arguments[1] != "plan" && arguments[1] != "apply" {
		return safeerr.New("usage: catalogctl repair idempotency <list|plan|apply> --tenant <uuid> [--reservation <uuid> --reason <text> --plan-digest <sha256:...>] [--limit <1-100>] [--config <path>]")
	}
	action := arguments[1]
	flags := diagnostic.Flags("catalogctl repair idempotency " + action)
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	tenantID := flags.String("tenant", "", "tenant UUID that owns the reservation")
	reservationID := flags.String("reservation", "", "opaque reservation UUID from safe diagnostics")
	reason := flags.String("reason", "", "operator reason recorded in audit evidence")
	planDigest := flags.String("plan-digest", "", "exact digest returned by the reviewed dry-run plan")
	limit := flags.Int("limit", 50, "maximum pending reservations returned by list")
	if err := diagnostic.Parse(flags, arguments[2:]); err != nil {
		return err
	}
	limitSet := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "limit" {
			limitSet = true
		}
	})
	invalidList := action == "list" && (*reservationID != "" || *reason != "" || *planDigest != "")
	invalidPlan := action == "plan" && (*reservationID == "" || *reason == "" || *planDigest != "" || limitSet)
	invalidApply := action == "apply" && (*reservationID == "" || *reason == "" || *planDigest == "" || limitSet)
	if flags.NArg() != 0 || *tenantID == "" || invalidList || invalidPlan || invalidApply {
		return safeerr.New("usage: catalogctl repair idempotency <list|plan|apply> --tenant <uuid> [--reservation <uuid> --reason <text> --plan-digest <sha256:...>] [--limit <1-100>] [--config <path>]")
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return err
	}
	if configuration.Database.Driver != "postgres" {
		return safeerr.New("idempotency repair requires database.driver=postgres")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, configuration.Database.DSN, databasePoolConfig(configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	if action == "list" {
		report, err := database.ListPendingIdempotencyReservations(ctx, pool, *tenantID, *limit)
		if err != nil {
			return err
		}
		return printJSON(report)
	}
	if action == "plan" {
		plan, err := database.PlanIdempotencyRepair(ctx, pool, *tenantID, *reservationID, *reason)
		if err != nil {
			return err
		}
		return printJSON(plan)
	}
	result, err := database.ApplyIdempotencyRepair(ctx, pool, *tenantID, *reservationID, *reason, *planDigest)
	if err != nil {
		return err
	}
	return printJSON(result)
}

// repairSearchProjections validates one attributed tenant/batch target before rebuilding derived search state.
func repairSearchProjections(arguments []string) error {
	const usage = "usage: catalogctl repair search-projections --tenant <uuid> --reason <text> [--operator-label <text>] [--limit <1-1000>] [--config <path>]"
	flags := diagnostic.Flags("catalogctl repair search-projections")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	tenantID := flags.String("tenant", "", "tenant UUID whose derived projections are rebuilt")
	reason := flags.String("reason", "", "operator reason recorded for each non-empty repair batch")
	operatorLabel := flags.String("operator-label", "", "optional bounded operator or change-record label")
	limit := flags.Int("limit", 500, "maximum observations rebuilt in this batch")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || database.ValidateCatalogSearchProjectionRepairInput(*tenantID, *limit, *reason, *operatorLabel) != nil {
		return safeerr.New(usage)
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return err
	}
	if configuration.Database.Driver != "postgres" {
		return safeerr.New("search projection repair requires database.driver=postgres")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, configuration.Database.DSN, databasePoolConfig(configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	result, err := database.RepairCatalogSearchProjections(ctx, pool, *tenantID, *limit, *reason, *operatorLabel)
	if err != nil {
		return err
	}
	return printJSON(result)
}

// migrate separates migration planning from forward apply plus exact API/worker privilege reconciliation.
func migrate(arguments []string) error {
	const usage = "usage: catalogctl migrate <plan|apply> [--config <path>] [--api-role <database-role> --worker-role <database-role>] [--semantic]"
	if len(arguments) < 1 || arguments[0] != "plan" && arguments[0] != "apply" {
		return safeerr.New(usage)
	}
	action := arguments[0]
	flags := diagnostic.Flags("catalogctl migrate " + action)
	semantic := flags.Bool("semantic", false, "include optional semantic schema; requires administrator-installed pgvector")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	apiRole := flags.String("api-role", "catalog_api", "PostgreSQL role used by the Catalog API")
	workerRole := flags.String("worker-role", "catalog_worker", "PostgreSQL role used by Catalog workers")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return safeerr.New(usage)
	}
	if err := database.ValidateRuntimeDatabaseRoles(*apiRole, *workerRole); err != nil {
		return err
	}
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return err
	}
	if configuration.Database.Driver != "postgres" {
		return safeerr.New("migrations require database.driver=postgres")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, configuration.Database.DSN, databasePoolConfig(configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	var result any
	if action == "apply" {
		applied, privileges, applyErr := database.ApplyAndReconcileRuntimePrivileges(ctx, pool, *apiRole, *workerRole, *semantic)
		if applyErr != nil {
			return applyErr
		}
		result = map[string]any{"applied": applied, "runtimePrivileges": privileges}
	} else {
		result, err = database.Plan(ctx, pool)
		if err == nil && *semantic {
			optional, optionalErr := database.PlanSemanticSchema(ctx, pool)
			if optionalErr != nil {
				return optionalErr
			}
			result = map[string]any{"core": result, "semantic": optional}
		}
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}

// validateConfiguration checks role-specific configuration and secret availability without building a runtime.
func validateConfiguration(arguments []string) error {
	if len(arguments) < 1 || arguments[0] != "validate" {
		return safeerr.New("usage: catalogctl config validate [--config <path>] [--role <api|worker|all|migration>]")
	}
	flags := diagnostic.Flags("catalogctl config validate")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	roleName := flags.String("role", string(config.RoleAPI), "process role to validate: api, worker, all, or migration")
	if err := diagnostic.Parse(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return safeerr.New("usage: catalogctl config validate [--config <path>] [--role <api|worker|all|migration>]")
	}
	role := config.Role(*roleName)
	configuration, err := config.LoadFor(*configPath, role)
	if err != nil {
		return err
	}
	if err := config.ValidateRuntimeSecrets(configuration, role); err != nil {
		return err
	}
	source := "environment and safe defaults"
	if *configPath != "" {
		source = *configPath + " plus environment overrides"
	}
	return printJSON(map[string]any{"status": "valid", "role": role, "source": source, "configuration": configuration.Report()})
}

// profiles verifies the embedded pinned standards bundle without network access.
func profiles(arguments []string) error {
	if len(arguments) != 1 || arguments[0] != "verify" {
		return safeerr.New("usage: catalogctl profiles verify")
	}
	report, err := schemas.VerifyBundle()
	if err != nil {
		return fmt.Errorf("verify standards profile bundle: %w", err)
	}
	return printJSON(report)
}

// diagnostics validates the selected runtime role and emits bounded redacted schema, profile and operational evidence.
func diagnostics(arguments []string) error {
	flags := diagnostic.Flags("catalogctl diagnostics")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	role := flags.String("role", "api", "runtime role to validate: api or worker")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return err
	}
	if *role != string(config.RoleAPI) && *role != string(config.RoleWorker) {
		return safeerr.New("diagnostics role must be api or worker")
	}
	ctx := context.Background()
	runtimeRole := config.Role(*role)
	runtime, err := platformruntime.Build(ctx, platformruntime.BuildOptions{ConfigPath: *configPath, Version: version, Commit: commit, SourceState: sourceState, Role: runtimeRole, LogOutput: os.Stderr})
	if err != nil {
		return err
	}
	defer runtime.Close()
	if err := runtime.Repository.Ping(ctx); err != nil {
		return err
	}
	schemaVersion, err := database.EmbeddedSchemaVersion()
	if err != nil {
		return err
	}
	profiles, err := schemas.VerifyBundle()
	if err != nil {
		return err
	}
	diagnosticContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	operations, snapshotErr := runtime.Observability.Snapshot(diagnosticContext)
	status := "ready"
	if snapshotErr != nil {
		status = "degraded"
	}
	return printBoundedJSON(map[string]any{
		"status": status, "role": runtimeRole, "version": version, "commit": commit,
		"schema":     map[string]any{"embeddedVersion": schemaVersion, "runtimePrivilegeManifestVersion": database.RuntimePrivilegeManifestVersion(), "startupCompatible": true},
		"profiles":   map[string]any{"ard": runtime.Config.Publication.ARDProfile, "aiCatalog": runtime.Config.Publication.AICatalogProfile, "bundleDigest": profiles.BundleDigest, "artifactCount": profiles.ArtifactCount},
		"operations": operations, "configuration": runtime.Config.Report(), "workLimits": effectiveWorkLimits(runtime.Config),
	}, maxDiagnosticsBytes)
}

// bootstrap creates initial namespace/publisher state using an authenticated configured development administrator.
func bootstrap(arguments []string) error {
	flags := diagnostic.Flags("catalogctl bootstrap")
	configPath := flags.String("config", "", "optional path to a JSON configuration baseline")
	namespaceSlug := flags.String("namespace", "default", "initial namespace slug")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return err
	}
	ctx := context.Background()
	runtime, err := platformruntime.Build(ctx, platformruntime.BuildOptions{ConfigPath: *configPath, Version: version, Commit: commit, SourceState: sourceState, Role: config.RoleAPI, LogOutput: os.Stderr})
	if err != nil {
		return err
	}
	defer runtime.Close()
	token := firstAdministratorToken(runtime.Config)
	if token == "" {
		return safeerr.New("bootstrap requires a configured development tenant administrator; production bootstrap uses the identity provisioning workflow")
	}
	actor, err := runtime.Authenticator.Authenticate(ctx, token)
	if err != nil {
		return safeerr.New("bootstrap development administrator could not be resolved to an active Catalog membership")
	}
	ctx = identity.WithActor(ctx, actor)
	if _, err := identity.RequirePermission(ctx, "access.manage", ""); err != nil {
		return safeerr.New("bootstrap requires current development access administration authority")
	}
	namespace, err := runtime.Catalog.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: *namespaceSlug, DisplayName: "Default"})
	if err != nil {
		return err
	}
	if len(runtime.Config.Discovery.Hosts) == 0 {
		return safeerr.New("bootstrap requires one configured discovery host")
	}
	publicationDomain := runtime.Config.Discovery.Hosts[0].PublisherDomain
	publisher, err := runtime.Catalog.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Example Publisher", Domains: []string{publicationDomain}})
	if err != nil {
		return err
	}
	publisher, err = runtime.Catalog.ApprovePublisherPublicationDomain(ctx, publisher.ID, publicationDomain, "Initial deployment publication approval", publisher.Revision)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"tenantId": actor.TenantID, "namespace": namespace, "publisher": publisher})
}

// firstAdministratorToken selects the lexically first configured token with tenant-wide administrator authority.
func firstAdministratorToken(configuration config.Config) string {
	keys := make([]string, 0, len(configuration.Auth.DevelopmentTokens))
	for token := range configuration.Auth.DevelopmentTokens {
		keys = append(keys, token)
	}
	sort.Strings(keys)
	for _, token := range keys {
		user := configuration.Auth.DevelopmentTokens[token]
		if slices.Contains(user.Roles, identity.RoleTenantAdmin) || slices.Contains(user.RoleTemplates, "tenant_admin") {
			return token
		}
	}
	return ""
}

// printJSON writes an indented JSON value to stdout and propagates encoding/output failures.
func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// printBoundedJSON checks the complete encoded report and trailing newline against the output ceiling before writing.
func printBoundedJSON(value any, maximum int) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if maximum <= 0 || len(raw)+1 > maximum {
		return fmt.Errorf("JSON report exceeds its %d-byte output limit", maximum)
	}
	_, err = fmt.Fprintln(os.Stdout, string(raw))
	return err
}

// reportError preserves the remote client's deliberately bounded JSON diagnostic
// contract; every other command uses the safe terminal projection.
func reportError(output io.Writer, err error) {
	var remote remoteAPIError
	if !errors.Is(err, safeerr.ErrPanic) && errors.As(err, &remote) {
		_, _ = fmt.Fprintln(output, remote.Error())
		return
	}
	diagnostic.Report(output, "catalogctl", string(catalog.ErrorCodeOf(err)), err, false)
}
