//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// This gate owns a fresh compose.integration.yaml database. It tests executable
// command/composition boundaries; repository fault matrices stay in database.
func TestPostgresProcesses(t *testing.T) {
	timeout := 3 * time.Minute
	if os.Getenv("CATALOG_TEST_PGBOUNCER_WRITER_LOSS") == "abrupt" {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	dsn := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	address, err := url.Parse(dsn)
	if err != nil || address.Scheme != "postgres" || address.Hostname() != "127.0.0.1" || address.Path != "/catalog_test" {
		t.Fatal("process integration requires the owned loopback catalog_test fixture")
	}
	owner, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect to owned PostgreSQL fixture failed")
	}
	defer func() { _ = owner.Close(context.Background()) }()
	var empty bool
	if err := owner.QueryRow(ctx, "SELECT to_regclass('public.catalog_schema_migrations') IS NULL").Scan(&empty); err != nil || !empty {
		t.Fatal("process integration requires a fresh unmigrated database")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile("../../deployments/postgres/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, strings.ReplaceAll(string(setup), "ON DATABASE catalog TO", "ON DATABASE catalog_test TO")); err != nil {
		t.Fatal("initialize owned runtime roles failed")
	}
	directory := t.TempDir()
	arguments := []string{"build", "-o", directory + string(os.PathSeparator)}
	coverageDirectory := os.Getenv("CATALOG_PROCESS_COVERAGE_DIR")
	if coverageDirectory != "" {
		arguments = append(arguments, "-cover", "-covermode=atomic", "-coverpkg=github.com/open-agentic-catalog/open-agentic-catalog/cmd/catalogctl,github.com/open-agentic-catalog/open-agentic-catalog/cmd/catalogd,github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime")
	}
	arguments = append(arguments, "./cmd/catalogctl", "./cmd/catalogd")
	build := exec.CommandContext(ctx, "go", arguments...) // #nosec G204,G702 -- fixed repository packages and a test-owned output directory.
	build.Dir = root
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build process fixtures: %v\n%s", err, output)
	}
	fixture := processFixture{t: t, directory: directory, coverageDirectory: coverageDirectory, ownerDSN: dsn}
	raw, err := os.ReadFile("../../config/postgres.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	decodeProcessJSON(t, raw, &cfg)
	fixture.tenantID = cfg.Discovery.Hosts[0].TenantID
	settingsPeer := configureProcessTenantSettings(t, fixture, &cfg)
	if os.Getenv("CATALOG_TEST_PGBOUNCER_WRITER_LOSS") == "abrupt" {
		// Pending public outbox evidence requires dispatch to be enabled. No
		// endpoint is registered, so this adds no receiver or outbound destination.
		cfg.Webhooks.Enabled = true
	}
	cfg.Database.DSN = ""
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") != "" {
		cfg.Database.MaxConnections = 4
	}
	cfg.Database.MaxConnLifetime.Duration = 2 * time.Second
	cfg.Database.MaxConnIdleTime.Duration = 500 * time.Millisecond
	cfg.Database.HealthCheckPeriod.Duration = 100 * time.Millisecond
	cfg.HTTP.Address = processAddress(ctx, t)
	cfg.HTTP.PublicBaseURL = "http://" + cfg.HTTP.Address
	cfg.Worker.OperationsAddress = processAddress(ctx, t)
	configuration := fixture.configuration("runtime", cfg)
	ctl := func(output any, args ...string) {
		t.Helper()
		command := fixture.command(ctx, "catalogctl", dsn, append(args, "--config", configuration)...)
		result := fixture.run(ctx, command, 0)
		decodeProcessJSON(t, result, output)
	}
	var plan []database.Migration
	ctl(&plan, "migrate", "plan")
	if len(plan) == 0 {
		t.Fatal("fresh migration plan is empty")
	}
	for _, item := range plan {
		if item.Applied {
			t.Fatal("fresh migration plan reports applied migration")
		}
	}
	var applied struct {
		Applied           []database.Migration              `json:"applied"`
		RuntimePrivileges []database.RuntimePrivilegeReport `json:"runtimePrivileges"`
	}
	ctl(&applied, "migrate", "apply")
	if len(applied.Applied) != len(plan) || len(applied.RuntimePrivileges) != 2 {
		t.Fatal("migration apply lost migrations or privilege reports")
	}
	roles := map[string]string{"api": "catalog_api", "worker": "catalog_worker"}
	for _, report := range applied.RuntimePrivileges {
		if !report.Compliant || report.DatabaseRole != roles[report.RuntimeRole] || len(report.Differences) != 0 {
			t.Fatal("runtime privilege reconciliation mismatch")
		}
		delete(roles, report.RuntimeRole)
	}
	if len(roles) != 0 {
		t.Fatal("missing runtime privilege manifest")
	}
	var retained []database.Migration
	ctl(&retained, "migrate", "plan")
	for i := range plan {
		plan[i].Applied = true
	}
	if !reflect.DeepEqual(retained, plan) {
		t.Fatal("applied migration ledger changed version/checksum or lost entries")
	}
	ctl(&applied, "migrate", "apply")
	if len(applied.Applied) != 0 || len(applied.RuntimePrivileges) != 2 || !applied.RuntimePrivileges[0].Compliant || !applied.RuntimePrivileges[1].Compliant {
		t.Fatal("migration reapply changed schema or lost grants")
	}
	var provisioning struct {
		Total       int                                   `json:"total"`
		Provisioned []database.IdentityProvisioningResult `json:"provisioned"`
	}
	ctl(&provisioning, "identity", "provision-development")
	if provisioning.Total != len(cfg.Auth.DevelopmentTokens) || len(provisioning.Provisioned) != provisioning.Total {
		t.Fatal("development provisioning did not report every configured principal")
	}
	qualifyDevelopmentProvisioningProcess(t, ctx, owner, fixture, cfg, dsn)
	tenant := cfg.Discovery.Hosts[0].TenantID
	realmCfg := cfg
	realmCfg.Discovery.Hosts = append([]config.DiscoveryHost(nil), cfg.Discovery.Hosts...)
	for i := range realmCfg.Discovery.Hosts {
		realmCfg.Discovery.Hosts[i].Mode = "disabled"
	}
	realmConfiguration := fixture.configuration("realms", realmCfg)
	realmList := func() identity.Realm {
		t.Helper()
		var page database.IdentityRealmPage
		decodeProcessJSON(t, fixture.run(ctx, fixture.command(ctx, "catalogctl", dsn, "identity", "realms", "list", "--tenant", tenant, "--config", realmConfiguration), 0), &page)
		if page.Total != 1 || len(page.Items) != 1 || page.NextAfter != "" || page.Items[0].TenantID != tenant {
			t.Fatal("unexpected development realm page")
		}
		return page.Items[0]
	}
	realm := realmList()
	for _, transition := range []struct{ action, state string }{{"suspend", "suspended"}, {"reactivate", "active"}} {
		args := []string{"identity", "realms", transition.action, "--tenant", tenant, "--realm", realm.ID, "--revision", strconv.FormatInt(realm.Revision, 10), "--operator", "owned-process-test", "--reason", "Verify operator composition", "--config", realmConfiguration}
		var result database.IdentityRealmTransitionResult
		decodeProcessJSON(t, fixture.run(ctx, fixture.command(ctx, "catalogctl", dsn, args...), 0), &result)
		if result.Realm.ID != realm.ID || string(result.Realm.State) != transition.state || result.Realm.Revision != realm.Revision+1 || result.TenantAccessSuspended != (transition.state == "suspended") {
			t.Fatal("realm transition report mismatch")
		}
		realm = result.Realm
		var before, after int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM audit_events").Scan(&before); err != nil {
			t.Fatal("count retained audit before stale transition failed")
		}
		// Terminal output retains a safe stage/code, not the private database
		// error. The retained state and audit checks below prove this refusal.
		fixture.run(ctx, fixture.command(ctx, "catalogctl", dsn, args...), 1, "catalogctl: identity: INTERNAL: operation failed; check configuration and dependency availability")
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM audit_events").Scan(&after); err != nil || before != after || !reflect.DeepEqual(realmList(), realm) {
			t.Fatal("stale realm transition changed retained state or audit")
		}
	}
	roleDSN := func(role string) string {
		runtimeAddress := *address
		if pooled := os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"); pooled != "" {
			parsed, parseErr := url.Parse(pooled)
			if parseErr != nil || parsed.Scheme != "postgres" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "/catalog_test" || os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT") == "" {
				t.Fatal("pooler route requires the explicitly owned loopback fixture")
			}
			runtimeAddress = *parsed
		}
		runtimeAddress.User = url.UserPassword("catalog_"+role, "catalog-"+role)
		return runtimeAddress.String()
	}
	// Inspect actual composed pools, then exercise the same settings through
	// compiled API/worker and operator commands below.
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker} {
		poolCfg := cfg
		poolCfg.Database.DSN = roleDSN(string(role))
		composed, err := platformruntime.Build(ctx, platformruntime.BuildOptions{Role: role, ConfigPath: fixture.configuration("pool-"+string(role), poolCfg), LogOutput: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		actual := composed.Pool.Config()
		composed.Close()
		if actual.MaxConnLifetime != 2*time.Second || actual.MaxConnIdleTime != 500*time.Millisecond || actual.HealthCheckPeriod != 100*time.Millisecond {
			t.Fatalf("%s pool ignored configured timing policy", role)
		}
	}
	// Admit exact roles through the real composition root, not owner authority.
	stop := fixture.start(ctx, "api", roleDSN("api"), configuration, cfg.HTTP.Address)
	peerConfig := cfg
	peerConfig.HTTP.Address = processAddress(ctx, t)
	peerConfig.HTTP.PublicBaseURL = "http://" + peerConfig.HTTP.Address
	peerConfiguration := fixture.configuration("peer", peerConfig)
	stopPeer := fixture.start(ctx, "api", roleDSN("api"), peerConfiguration, peerConfig.HTTP.Address)
	var namespace catalog.Namespace
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/namespaces", "local-admin", `{"slug":"process-fixture","displayName":"Process fixture"}`, 201), &namespace)
	if namespace.ID == "" || namespace.TenantID != tenant || namespace.Slug != "process-fixture" {
		t.Fatal("namespace creation response mismatch")
	}
	stop = qualifyProcessTenantSettings(ctx, t, fixture, owner, cfg, peerConfig, stop, roleDSN("api"), roleDSN("worker"), settingsPeer)
	qualifyProcessImport(ctx, t, fixture, owner, dsn, cfg.HTTP.Address, peerConfig.HTTP.Address, tenant, namespace.ID)
	// Qualify ratings composition through compiled processes and exact runtime
	// logins. Repository tests own the complete vote and publication fault matrix.
	checkRatings := func(revision int64) {
		t.Helper()
		for _, endpoint := range []string{cfg.HTTP.Address, peerConfig.HTTP.Address} {
			var page struct {
				Items      []ratings.Record `json:"items"`
				NextCursor string           `json:"nextCursor"`
			}
			decodeProcessJSON(t, fixture.request(ctx, endpoint, "GET", "/api/v1/ratings?limit=25", "local-reader", "", 200), &page)
			if page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatal("fresh human rating inventory is not an empty terminal page")
			}
			var choice ratings.Sharing
			decodeProcessJSON(t, fixture.request(ctx, endpoint, "GET", "/api/v1/ratings/sharing?host=127.0.0.1&audience=anonymous", "local-admin", "", 200), &choice)
			if choice.TenantID != tenant || choice.Host != "127.0.0.1" || choice.Audience != "anonymous" || choice.Enabled || choice.Revision != revision {
				t.Fatal("cross-process rating sharing choice mismatch")
			}
		}
	}
	checkRatings(0)
	sharingInput := `{"host":"127.0.0.1","audience":"anonymous","enabled":false,"reason":"Keep process fixture ratings private"}`
	sharingReply := fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/ratings/sharing", "local-admin", sharingInput, 200)
	checkRatings(1)
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") != "" {
		// Matched bounded SQL reads record overhead without a host-sensitive
		// performance threshold. Both use exact runtime identities and real RLS.
		for _, route := range []string{"direct", "pooled"} {
			for _, role := range []string{"api", "worker"} {
				selected := roleDSN(role)
				if route == "direct" {
					direct := *address
					direct.User = url.UserPassword("catalog_"+role, "catalog-"+role)
					selected = direct.String()
				}
				pool, err := database.OpenRuntimeObserved(ctx, selected, database.PoolConfig{MaxConnections: 1, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthCheckPeriod: time.Second}, nil)
				if err != nil {
					t.Fatal("matched runtime read baseline admission failed")
				}
				started := time.Now()
				for i := 0; i < 16; i++ {
					tx, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal("matched runtime read begin failed")
					}
					if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id',$1,true)", tenant); err != nil {
						t.Fatal("matched read tenant context failed")
					}
					if role == "api" {
						var retained string
						if err := tx.QueryRow(ctx, "SELECT slug FROM namespaces WHERE tenant_id=$1 AND id=$2", tenant, namespace.ID).Scan(&retained); err != nil || retained != namespace.Slug {
							t.Fatal("matched API read lost its retained namespace")
						}
					} else {
						var count int
						if err := tx.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE tenant_id=$1", tenant).Scan(&count); err != nil || count < 0 {
							t.Fatal("matched worker Job read failed")
						}
					}
					if err := tx.Commit(ctx); err != nil {
						t.Fatal("matched read commit failed")
					}
				}
				t.Logf("matched %s %s: reads=16 duration=%s", route, role, time.Since(started))
				pool.Close()
			}
		}
	}
	// New administration uses the real process, API login, command coordinator,
	// audit and current joins. No new container or authentication fixture.
	var accessRoles identity.AuthorizationPage[identity.RoleDefinition]
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/access/roles?limit=100", "local-admin", "", 200), &accessRoles)
	administratorRole := ""
	for _, role := range accessRoles.Items {
		if role.Key == "tenant_admin" {
			administratorRole = role.ID
		}
	}
	if administratorRole == "" {
		t.Fatal("protected administrator template absent")
	}
	var reader struct {
		MembershipID string `json:"membershipId"`
	}
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/me", "local-reader", "", 200), &reader)
	var customRole identity.RoleDefinition
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/roles", "local-admin", `{"name":"Process validator","permissions":["resource.read_content","resource.validate"],"reason":"Qualify current group permissions"}`, 201), &customRole)
	var group identity.AccessGroup
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/groups", "local-admin", `{"name":"Process team","reason":"Qualify same-realm group"}`, 201), &group)
	var groupMember identity.GroupMember
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/groups/"+group.ID+"/members", "local-admin", `{"membershipId":"`+reader.MembershipID+`","reason":"Add real process member"}`, 201), &groupMember)
	fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/assignments", "local-admin", `{"roleDefinitionId":"`+customRole.ID+`","groupId":"`+group.ID+`","namespaceId":"`+namespace.ID+`","reason":"Qualify scoped group assignment"}`, 201)
	checkCurrent := func(want bool) {
		t.Helper()
		for _, endpoint := range []string{cfg.HTTP.Address, peerConfig.HTTP.Address} {
			var decisions identity.PermissionDecision
			decodeProcessJSON(t, fixture.request(ctx, endpoint, "POST", "/api/v1/access/capabilities", "local-reader", `{"checks":[{"permission":"resource.validate","namespaceId":"`+namespace.ID+`"},{"permission":"resource.edit","namespaceId":"`+namespace.ID+`"}]}`, 200), &decisions)
			if !reflect.DeepEqual(decisions.Allowed, []bool{want, false}) {
				t.Fatal("cross-process current capability decisions", decisions)
			}
		}
	}
	checkCurrent(true)
	// Both already-running processes must immediately use edits written by the
	// other process, without replacing their principal or restarting a runtime.
	for index, permissions := range []string{`["resource.read_content"]`, `["resource.read_content","resource.validate"]`} {
		endpoint := []string{peerConfig.HTTP.Address, cfg.HTTP.Address}[index]
		var edited identity.RoleDefinition
		decodeProcessJSON(t, fixture.request(ctx, endpoint, "PATCH", "/api/v1/access/roles/"+customRole.ID, "local-admin", `{"name":"Process validator","permissions":`+permissions+`,"reason":"Qualify cross-process current role edit"}`, 200, map[string]string{"Idempotency-Key": "process-role-edit-" + strconv.Itoa(index), "If-Match": `"rev-` + strconv.Itoa(index+1) + `"`}), &edited)
		if edited.ID != customRole.ID || edited.Revision != int64(index+2) {
			t.Fatal("cross-process role edit changed identity or revision")
		}
		checkCurrent(index == 1)
	}
	// Use a different command key for the second assignment on this route.
	var temporaryAdmin identity.AccessAssignment
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/assignments", "local-admin", `{"roleDefinitionId":"`+administratorRole+`","membershipId":"`+reader.MembershipID+`","reason":"Qualify temporary direct administrator"}`, 201, map[string]string{"Idempotency-Key": "temporary-administration"}), &temporaryAdmin)
	privateInput := `{"name":"Retained process role","permissions":["resource.read"],"reason":"Qualify durable private replay"}`
	privateReply := fixture.request(ctx, peerConfig.HTTP.Address, "POST", "/api/v1/access/roles", "local-reader", privateInput, 201)
	var privateRole identity.RoleDefinition
	decodeProcessJSON(t, privateReply, &privateRole)

	stop()
	stop = fixture.start(ctx, "api", roleDSN("api"), configuration, cfg.HTTP.Address)
	checkRatings(1)
	if replay := fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/ratings/sharing", "local-admin", sharingInput, 200); !bytes.Equal(replay, sharingReply) {
		t.Fatal("rating sharing replay changed across API process replacement")
	}
	checkRatings(1)
	var readback catalog.Namespace
	decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/namespaces/"+namespace.ID, "local-admin", "", 200), &readback)
	if readback.ID != namespace.ID || readback.Slug != namespace.Slug || readback.Revision != namespace.Revision {
		t.Fatal("namespace did not persist across API process replacement")
	}
	fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/namespaces/"+namespace.ID, "other-tenant-admin", "", 404)
	// A new API process has no local response cache: exact reply/audit checks
	// exercise PostgreSQL atomic replay, then current permission denial.
	replay := fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/roles", "local-reader", privateInput, 201)
	if !bytes.Equal(replay, privateReply) {
		t.Fatal("durable private response changed across restart")
	}
	var auditCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='authorization.role.created' AND target_id=$2`, tenant, privateRole.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatal("replay duplicated authorization audit", err)
	}
	fixture.request(ctx, peerConfig.HTTP.Address, "POST", "/api/v1/access/assignments/"+temporaryAdmin.ID+":revoke", "local-admin", `{"reason":"Revoke temporary direct administration"}`, 200, map[string]string{"If-Match": `"rev-1"`})
	for _, endpoint := range []string{cfg.HTTP.Address, peerConfig.HTTP.Address} {
		refused := fixture.request(ctx, endpoint, "POST", "/api/v1/access/roles", "local-reader", privateInput, 403)
		if bytes.Contains(refused, []byte("Retained process role")) {
			t.Fatal("revoked caller received cross-process private replay")
		}
	}
	fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/groups/"+group.ID+"/members/"+groupMember.ID+":revoke", "local-admin", `{"reason":"Revoke process group path"}`, 200, map[string]string{"If-Match": `"rev-1"`})
	checkCurrent(false)

	if os.Getenv("CATALOG_TEST_PGBOUNCER_WRITER_LOSS") == "abrupt" {
		qualifyAbruptPooledWriterLoss(ctx, t, fixture, owner, cfg, peerConfig, roleDSN, settingsPeer, root)
	} else if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") != "" {
		workerConfig := cfg
		workerConfig.Worker.OperationsAddress = processAddress(ctx, t)
		stopWorker := fixture.start(ctx, "worker", roleDSN("worker"), fixture.configuration("pooled-worker-one", workerConfig), workerConfig.Worker.OperationsAddress)
		workerConfig.Worker.OperationsAddress = processAddress(ctx, t)
		stopSecondWorker := fixture.start(ctx, "worker", roleDSN("worker"), fixture.configuration("pooled-worker-two", workerConfig), workerConfig.Worker.OperationsAddress)
		// Reuse the maintained executable governance, publication, search,
		// pagination, job and replay assertions rather than mirror its journey.
		fixture.run(ctx, fixture.command(ctx, "catalogctl", "", "smoke", "--base-url", "http://"+cfg.HTTP.Address, "--run-id", "pooled-process", "--timeout", "2m"), 0)
		direct := *address
		direct.User = url.UserPassword("catalog_api", "catalog-api")
		fixture.pgbouncerHTTPReadBenchmark(ctx, root, cfg, direct.String())
		rows, err := owner.Query(ctx, "SELECT usename,count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker') GROUP BY usename ORDER BY usename")
		if err != nil {
			t.Fatal("observe warm runtime backend counts")
		}
		counts := make(map[string]int)
		for rows.Next() {
			var role string
			var count int
			if err := rows.Scan(&role, &count); err != nil {
				t.Fatal("read runtime backend counts")
			}
			counts[role] = count
		}
		rows.Close()
		if rows.Err() != nil || counts["catalog_api"] < 1 || counts["catalog_worker"] < 1 || counts["catalog_api"] > 12 || counts["catalog_worker"] > 12 {
			t.Fatal("warm replicas lost separate bounded backend budgets")
		}
		t.Logf("warm pooled backends: api=%d worker=%d", counts["catalog_api"], counts["catalog_worker"])
		var plugin catalog.Resource
		decodeProcessJSON(t, fixture.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/resources", "local-admin", `{"namespaceId":"`+namespace.ID+`","canonicalIdentifier":"urn:air:catalog.example.com:plugin:pooled-plugin","kind":"plugin"}`, 201), &plugin)
		fixture.request(ctx, peerConfig.HTTP.Address, "GET", "/api/v1/resources/"+plugin.ID, "other-tenant-admin", "", 404)
		for _, service := range []string{"pooler", "postgres"} {
			poolerCompose(t, ctx, "stop", service)
			fixture.request(ctx, cfg.HTTP.Address, "GET", "/livez", "", "", 200)
			fixture.request(ctx, cfg.HTTP.Address, "GET", "/readyz", "", "", 503)
			poolerCompose(t, ctx, "start", service)
			fixture.awaitHealthy(ctx, cfg.HTTP.Address)
			fixture.awaitHealthy(ctx, peerConfig.HTTP.Address)
			fixture.awaitHealthy(ctx, workerConfig.Worker.OperationsAddress)
			fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/namespaces/"+namespace.ID, "local-admin", "", 200)
			fixture.request(ctx, peerConfig.HTTP.Address, "POST", "/api/v1/access/roles", "local-reader", privateInput, 403)
		}

		promotion := exec.CommandContext(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PYTHON"), "-B", filepath.Join(root, "test", "pgbouncer", "writer_recovery.py")) // #nosec G204,G702 -- exact invocation-owned qualification helper.
		promotion.Env = append(os.Environ(), "CATALOG_TEST_PGBOUNCER_API=http://"+cfg.HTTP.Address)
		if err := promotion.Run(); err != nil {
			t.Fatal("owned pooled writer promotion failed")
		}
		awaitFreshPooledRoles(ctx, t, roleDSN)
		fixture.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/namespaces/"+namespace.ID, "local-admin", "", 200)
		fixture.request(ctx, peerConfig.HTTP.Address, "POST", "/api/v1/access/roles", "local-reader", privateInput, 403)

		stopSecondWorker()
		stopWorker()
		qualifyPooledRoleRecovery(ctx, t, fixture, roleDSN, configuration, cfg.HTTP.Address)
	}

	stopPeer()
	stop()
	stop = fixture.start(ctx, "worker", roleDSN("worker"), configuration, cfg.Worker.OperationsAddress)
	fixture.request(ctx, cfg.Worker.OperationsAddress, "GET", "/api/v1/namespaces", "local-admin", "", 404)
	fixture.request(ctx, cfg.Worker.OperationsAddress, "GET", "/api/v1/ratings", "local-admin", "", 404)
	stop()
	// A worker login cannot be admitted as an API runtime.
	fixture.run(ctx, fixture.command(ctx, "catalogd", roleDSN("worker"), "api", "--config", configuration), 1, `msg="operation failed; check configuration and dependency availability"`, "event=process.failed", "stage=startup", "error_code=INTERNAL")
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") != "" {
		t.Run("tls-renewal", func(t *testing.T) {
			qualified := fixture
			qualified.t = t
			qualifyPoolerTLSRenewal(t, qualified, cfg, settingsPeer, namespace.ID)
		})
	}
}

type processFixture struct {
	t                 *testing.T
	directory         string
	coverageDirectory string
	ownerDSN          string
	tenantID          string
}

func (f processFixture) configuration(name string, cfg config.Config) string {
	f.t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(f.directory, name+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f processFixture) command(ctx context.Context, binary, dsn string, args ...string) *exec.Cmd {
	f.t.Helper()
	command := exec.CommandContext(ctx, filepath.Join(f.directory, binary), args...) // #nosec G204,G702 -- only the two binaries built in this owned fixture.
	command.Env = []string{"CATALOG_DATABASE_DSN=" + dsn}
	command.WaitDelay = time.Second
	if f.coverageDirectory != "" {
		directory, err := os.MkdirTemp(f.coverageDirectory, binary+"-")
		if err != nil {
			f.t.Fatal(err)
		}
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	return command
}

func (f processFixture) counters(command *exec.Cmd) {
	f.t.Helper()
	for _, entry := range command.Env {
		if directory, ok := strings.CutPrefix(entry, "GOCOVERDIR="); ok {
			for _, pattern := range []string{"covmeta.*", "covcounters.*"} {
				files, err := filepath.Glob(filepath.Join(directory, pattern))
				if err != nil || len(files) == 0 {
					f.t.Fatalf("process omitted %s coverage", pattern)
				}
			}
		}
	}
}

func (f processFixture) run(ctx context.Context, command *exec.Cmd, exitCode int, diagnostic ...string) []byte {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil || command.ProcessState == nil || command.ProcessState.ExitCode() != exitCode {
		f.t.Fatalf("process exit: %v (expected %d, deadline: %v)", err, exitCode, ctx.Err())
	}
	if exitCode == 0 && stderr.Len() != 0 || exitCode != 0 && (stderr.Len() == 0 || stdout.Len() != 0) {
		f.t.Fatal("operator stdout/stderr contract mismatch")
	}
	if exitCode != 0 && (strings.Count(stderr.String(), "\n") != 1 || strings.Contains(stderr.String(), "catalog_worker") || strings.Contains(stderr.String(), "identity realm revision does not match")) {
		f.t.Fatal("terminal diagnostic duplicated or exposed a private cause")
	}
	for _, expected := range diagnostic {
		if !strings.Contains(stderr.String(), expected) {
			f.t.Fatal("process rejection omitted the expected diagnostic")
		}
	}
	f.counters(command)
	return stdout.Bytes()
}

func (f processFixture) start(ctx context.Context, role, dsn, configuration, address string) func() {
	f.t.Helper()
	stop, _ := f.startProcess(ctx, role, dsn, configuration, address)
	return stop
}

func (f processFixture) startProcess(ctx context.Context, role, dsn, configuration, address string) (func(), *os.Process) {
	f.t.Helper()
	command := f.command(ctx, "catalogd", dsn, role, "--config", configuration)
	var evidence processEvidenceWriter
	command.Stdout, command.Stderr = &evidence, &evidence
	command.Env = append(command.Env, "CATALOG_LOG_FORMAT=json")
	if err := command.Start(); err != nil {
		f.t.Fatalf("start %s process: %v", role, err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	stop := func() {
		f.t.Helper()
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			f.t.Fatalf("signal %s: %v", role, err)
		}
		select {
		case <-done:
			if waitErr != nil {
				f.t.Fatalf("%s graceful shutdown: %v", role, waitErr)
			}
		case <-time.After(25 * time.Second):
			f.t.Fatal("process graceful shutdown timed out")
		}
		f.counters(command)
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
		if err != nil {
			f.t.Fatalf("%s retained listener after exit: %v", role, err)
		}
		_ = listener.Close()
	}
	f.t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			f.t.Error("owned process cleanup timed out")
		}
	})
	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/readyz", nil)
		if err != nil {
			f.t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			var readiness struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&readiness)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && readiness.Status == "ready" {
				return stop, command.Process
			}
			if response.StatusCode != http.StatusServiceUnavailable {
				f.t.Fatalf("%s readiness contract mismatch", role)
			}
		}
		select {
		case <-done:
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			connection, probeErr := pgx.Connect(probeCtx, dsn)
			if connection != nil {
				var answer int
				probeErr = connection.QueryRow(probeCtx, "SELECT 1").Scan(&answer)
				_ = connection.Close(probeCtx)
			}
			cancel()
			var postgresError *pgconn.PgError
			_ = errors.As(probeErr, &postgresError)
			f.t.Logf("owned startup evidence: startupFailure=%t freshQuerySucceeded=%t cachedPoolerLogin=%t sqlState=%s", evidence.startupFailure(), probeErr == nil, cachedPoolerLogin(postgresError), processPostgresState(postgresError))
			f.t.Fatalf("%s exited before readiness: %v", role, waitErr)
		case <-deadline.C:
			f.t.Fatalf("%s did not become ready", role)
		case <-ctx.Done():
			f.t.Fatal("process fixture deadline expired")
		case <-poll.C:
		}
	}
}

func (f processFixture) request(ctx context.Context, address, method, path, token, body string, status int, extraHeaders ...map[string]string) []byte {
	f.t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "owned-process-test")
	}
	for _, headers := range extraHeaders {
		for name, value := range headers {
			request.Header.Set(name, value)
		}
	}
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		f.t.Fatal("owned process HTTP exchange failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode != status {
		f.observeAssignmentRefusal(ctx, path, request, response, raw)
		f.t.Fatalf("owned HTTP response: status=%d expected=%d, read=%v", response.StatusCode, status, err)
	}
	return raw
}

func decodeProcessJSON(t *testing.T, raw []byte, output any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(output); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		t.Fatal("process returned invalid JSON")
	}
}

func processAddress(ctx context.Context, t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// awaitHealthy polls only the selected process after an owned dependency outage.
func (f processFixture) awaitHealthy(ctx context.Context, address string) {
	f.t.Helper()
	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(30 * time.Second)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/readyz", nil)
		if err != nil {
			f.t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			f.t.Fatal("original process did not recover readiness")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
