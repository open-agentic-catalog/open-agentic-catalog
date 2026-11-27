//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

// The two private paths publish distinguishable entries with a common search term.
func configureProcessTenantSettings(t *testing.T, f processFixture, cfg *config.Config) string {
	t.Helper()
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request or credential at owned settings peer")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/ard+json")
		switch r.URL.Path {
		case "/paused-primary":
			_, _ = io.WriteString(w, `{"entries":[
				{"identifier":"urn:air:fixture.example:private:amber","displayName":"Ledger Amber","type":"application/a2a-agent-card+json","data":{"name":"Amber"}},
				{"identifier":"urn:air:fixture.example:private:cedar","displayName":"Ledger Cedar","type":"application/a2a-agent-card+json","data":{"name":"Cedar"}}]}`)
		case "/active-secondary":
			_, _ = io.WriteString(w, `{"entries":[
				{"identifier":"urn:air:fixture.example:private:birch","displayName":"Ledger Birch","type":"application/a2a-agent-card+json","data":{"name":"Birch"}}]}`)
		default:
			_, _ = io.WriteString(w, `{"entries":[]}`)
		}
	}))
	t.Cleanup(peer.Close)
	trustFile := filepath.Join(f.directory, "settings-peer-ca.pem")
	if err := os.WriteFile(trustFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Environment = "test"
	cfg.Worker.SourceMaintenanceInterval.Duration = time.Minute
	cfg.Worker.DependencyRetryInitialDelay.Duration = time.Second
	cfg.Worker.DependencyRetryCeiling.Duration = time.Second
	policy := config.FederationPolicy{SchemaVersion: 1, PolicyID: "process-settings", Revision: 1,
		Public:          config.FederationPublicPolicy{Mode: "allowlist", Origins: []string{}},
		PrivateProfiles: []config.FederationPrivateProfile{{Name: "process-settings", Origins: []string{peer.URL}, AddressRanges: []string{"127.0.0.1/32"}, AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000002"}, TrustBundleFile: trustFile, TransportRef: "direct"}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Federation.PolicyFile = filepath.Join(f.directory, "settings-policy.json")
	if err := os.WriteFile(cfg.Federation.PolicyFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return peer.URL
}

type settingsExchange struct {
	status  int
	body    []byte
	headers http.Header
	err     error
}

func processSettingsHTTP(ctx context.Context, client *http.Client, address, token, method, body, key, tag string) settingsExchange {
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+"/api/v1/tenant-settings", bytes.NewBufferString(body))
	if err != nil {
		return settingsExchange{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("If-Match", tag)
	}
	response, err := client.Do(request) // #nosec G704 -- fixed owned loopback API; redirects refused by the caller.
	if err != nil {
		return settingsExchange{err: err}
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if len(raw) > 8192 {
		err = fmt.Errorf("settings fixture response exceeds its bound")
	}
	return settingsExchange{response.StatusCode, raw, response.Header, err}
}

// Extend the maintained two-API fixture, without a second database launcher.
// Return the replacement API stop function so the original caller keeps owning it.
func qualifyProcessTenantSettings(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, cfg, peerCfg config.Config, stopAPI func(), apiDSN, workerDSN, peerURL string) func() {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	exchange := func(address, token, method, body, key, tag string, status int) settingsExchange {
		t.Helper()
		got := processSettingsHTTP(ctx, client, address, token, method, body, key, tag)
		if got.err != nil || got.status != status {
			t.Fatalf("settings HTTP status=%d expected=%d error=%v", got.status, status, got.err)
		}
		return got
	}
	read := func(address, token string) (tenantsettings.View, string) {
		t.Helper()
		got := exchange(address, token, http.MethodGet, "", "", "", 200)
		var view tenantsettings.View
		decodeProcessJSON(t, got.body, &view)
		tag := got.headers.Get("ETag")
		if tag == "" || got.headers.Get("Cache-Control") != "no-store" {
			t.Fatal("settings validator/cache contract")
		}
		return view, tag
	}
	serial := 0
	save := func(address, overrides string) tenantsettings.View {
		t.Helper()
		_, tag := read(address, "local-admin")
		serial++
		got := exchange(address, "local-admin", http.MethodPost, `{"overrides":`+overrides+`,"reason":"Owned process settings qualification"}`, "settings-process-"+strconv.Itoa(serial), tag, 200)
		var view tenantsettings.View
		decodeProcessJSON(t, got.body, &view)
		current, currentTag := read(address, "local-admin")
		if !bytes.Equal(got.body, mustSettingsJSON(t, current)) || got.headers.Get("ETag") != currentTag {
			t.Fatal("save/read settings evidence changed")
		}
		return view
	}
	tenant := cfg.Discovery.Hosts[0].TenantID
	saved := save(cfg.HTTP.Address, `{"automaticSourceSyncEnabled":false,"automaticSourceSyncInterval":"1m","newJobMaxAttempts":2}`)
	other, _ := read(peerCfg.HTTP.Address, "other-tenant-admin")
	if saved.Revision != 1 || other.Revision != 0 || other.Effective.NewJobMaxAttempts != cfg.Jobs.MaxAttempts {
		t.Fatal("initial isolation/defaults")
	}
	source := func(token, path, displayName string) federation.Source {
		t.Helper()
		var inspected federation.Inspection
		decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/federation/source-inspections", token, `{"url":"`+peerURL+`/`+path+`","networkProfile":"process-settings"}`, 201, map[string]string{"Idempotency-Key": "settings-inspect-" + path}), &inspected)
		var created federation.Source
		decodeProcessJSON(t, f.request(ctx, peerCfg.HTTP.Address, "POST", "/api/v1/federation/sources", token, `{"inspectionId":"`+inspected.ID+`","displayName":"`+displayName+`","retrievalMode":"indexed","updateMode":"automatic","scope":{"kinds":["agent"]},"networkProfile":"process-settings","reason":"Owned process source"}`, 201, map[string]string{"Idempotency-Key": "settings-source-" + path}), &created)
		return created
	}
	const sharedName = "Team directory"
	first := source("local-admin", "paused-primary", sharedName)
	second := source("other-tenant-admin", "active-secondary", sharedName)
	if !t.Run("private source isolation across tenants", func(t *testing.T) {
		qualifyPrivateSourceIsolation(ctx, t, f, cfg, peerCfg.HTTP.Address, sharedName, &first, &second)
	}) {
		t.FailNow()
	}
	workers := make([]config.Config, 2)
	stops := make([]func(), 2)
	for i := range workers {
		workers[i] = cfg
		workers[i].Worker.OperationsAddress = processAddress(ctx, t)
		stops[i] = f.start(ctx, "worker", workerDSN, f.configuration(fmt.Sprintf("settings-worker-%d", i), workers[i]), workers[i].Worker.OperationsAddress)
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	processIdentity := func(address, path string) string {
		t.Helper()
		var v struct {
			InstanceID string `json:"instanceId"`
			StartedAt  string `json:"startedAt"`
		}
		decodeProcessJSON(t, f.request(ctx, address, "GET", path, "local-admin", "", 200), &v)
		if v.InstanceID == "" || v.StartedAt == "" {
			t.Fatal("missing actual execution identity")
		}
		return v.InstanceID + "/" + v.StartedAt
	}
	identities := []string{processIdentity(cfg.HTTP.Address, "/api/v1/service-information"), processIdentity(peerCfg.HTTP.Address, "/api/v1/service-information"), processIdentity(workers[0].Worker.OperationsAddress, "/status"), processIdentity(workers[1].Worker.OperationsAddress, "/status")}
	assertIdentities := func() {
		t.Helper()
		got := []string{processIdentity(cfg.HTTP.Address, "/api/v1/service-information"), processIdentity(peerCfg.HTTP.Address, "/api/v1/service-information"), processIdentity(workers[0].Worker.OperationsAddress, "/status"), processIdentity(workers[1].Worker.OperationsAddress, "/status")}
		for i := range got {
			if got[i] != identities[i] {
				t.Fatal("settings change restarted a process")
			}
		}
	}
	await := func(label string, check func() bool) {
		t.Helper()
		deadline := time.NewTimer(35 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			complete := check()
			select {
			case <-ctx.Done():
				t.Fatal("settings process deadline", label)
			case <-deadline.C:
				t.Fatal("settings process condition completed too late", label)
			default:
			}
			if complete {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("settings process deadline", label)
			case <-deadline.C:
				t.Fatal("settings process condition not reached", label)
			case <-ticker.C:
			}
		}
	}
	countJobs := func(s federation.Source) int {
		t.Helper()
		var count int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1 AND payload->>'sourceId'=$2`, s.TenantID, s.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	awaitJob := func(s federation.Source, trigger string, limit int, expectedID string) string {
		t.Helper()
		var identifier string
		await("retained successful "+trigger+" sync", func() bool {
			var actual int
			var state string
			err := owner.QueryRow(ctx, `SELECT id::text,attempt_limit,state FROM jobs WHERE tenant_id=$1 AND kind='source.sync' AND payload->>'sourceId'=$2 AND payload->>'trigger'=$3 AND ($4='' OR id=nullif($4,'')::uuid) ORDER BY created_at DESC,id DESC LIMIT 1`, s.TenantID, s.ID, trigger, expectedID).Scan(&identifier, &actual, &state)
			if errors.Is(err, pgx.ErrNoRows) {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			if actual != limit {
				t.Fatalf("new %s Job used %d attempts, expected %d", trigger, actual, limit)
			}
			if state == "failed" || state == "dead_letter" {
				t.Fatal("owned source sync failed")
			}
			return state == "succeeded"
		})
		return identifier
	}
	secondaryJob := awaitJob(second, "automatic", cfg.Jobs.MaxAttempts, "")
	if countJobs(first) != 0 {
		t.Fatal("paused tenant admitted automatic work")
	}
	save(peerCfg.HTTP.Address, `{"automaticSourceSyncInterval":"1m","newJobMaxAttempts":3}`)
	automatic := awaitJob(first, "automatic", 3, "")
	save(cfg.HTTP.Address, `{"automaticSourceSyncEnabled":false,"automaticSourceSyncInterval":"1m","newJobMaxAttempts":4}`)
	manual := func(s federation.Source, address, key string, expected int) string {
		t.Helper()
		raw := f.request(ctx, address, "POST", "/api/v1/federation/sources/"+s.ID+":sync", "local-admin", `{}`, expected, map[string]string{"Idempotency-Key": key, "If-Match": `"rev-` + strconv.FormatInt(s.Revision, 10) + `"`})
		if expected != http.StatusAccepted {
			return ""
		}
		var accepted struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
			Existing bool `json:"existing"`
		}
		decodeProcessJSON(t, raw, &accepted)
		if !identity.ValidTenantID(accepted.Job.ID) || accepted.Existing {
			t.Fatal("manual sync did not return its exact admitted Job")
		}
		return accepted.Job.ID
	}
	manualID := manual(first, peerCfg.HTTP.Address, "settings-manual", 202)
	awaitJob(first, "manual", 4, manualID)
	primary := tenantSyncResults{"local-admin", first, []string{"Amber", "Cedar"}, []string{manualID, automatic}}
	secondary := tenantSyncResults{"other-tenant-admin", second, []string{"Birch"}, []string{secondaryJob}}
	t.Run("tenant-scoped indexed search and jobs", func(t *testing.T) {
		fixture := f
		fixture.t = t
		for _, pair := range [][2]tenantSyncResults{{primary, secondary}, {secondary, primary}} {
			own, foreign := pair[0], pair[1]
			qualifyTenantSearchPages(ctx, fixture, peerCfg.HTTP.Address, own, foreign)
			qualifyTenantJobPages(ctx, fixture, cfg.HTTP.Address, own, foreign)
		}
	})
	var retained int
	if err := owner.QueryRow(ctx, `SELECT attempt_limit FROM jobs WHERE tenant_id=$1 AND id=$2`, tenant, automatic).Scan(&retained); err != nil || retained != 3 {
		t.Fatal("retained budget changed", err)
	}
	// Temporarily make the policy table unavailable only in this owned database.
	// An otherwise valid admission must fail instead of inheriting defaults.
	before := countJobs(first)
	if _, err := owner.Exec(ctx, `ALTER TABLE tenant_settings RENAME TO unavailable_tenant_settings`); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if _, err := owner.Exec(ctx, `ALTER TABLE unavailable_tenant_settings RENAME TO tenant_settings`); err != nil {
				t.Fatal(err)
			}
		}()
		exchange(cfg.HTTP.Address, "local-admin", http.MethodGet, "", "", "", 503)
		manual(first, peerCfg.HTTP.Address, "settings-policy-read-outage", 503)
		if countJobs(first) != before {
			t.Fatal("policy read failure admitted fallback work")
		}
	}()
	read(peerCfg.HTTP.Address, "local-admin")
	// Two actual APIs compete for the same validator. No merge or lost update.
	_, tag := read(cfg.HTTP.Address, "local-admin")
	outcomes := make(chan settingsExchange, 2)
	for i, endpoint := range []string{cfg.HTTP.Address, peerCfg.HTTP.Address} {
		go func() {
			outcomes <- processSettingsHTTP(ctx, client, endpoint, "local-admin", http.MethodPost, fmt.Sprintf(`{"overrides":{"automaticSourceSyncEnabled":false,"newJobMaxAttempts":%d},"reason":"Concurrent settings edit"}`, i+5), fmt.Sprintf("settings-race-%d", i), tag)
		}()
	}
	wins, conflicts := 0, 0
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatal(got.err)
		}
		switch got.status {
		case 200:
			wins++
		case 412:
			conflicts++
		default:
			t.Fatal("unexpected concurrent status", got.status)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("concurrent settings lost update")
	}
	// The delegate's saved receipt and the administrator's current settings must be distinguishable.
	var role identity.RoleDefinition
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/roles", "local-admin", `{"name":"Settings delegate","permissions":["settings.read","settings.manage"],"reason":"Owned settings delegate"}`, 201, map[string]string{"Idempotency-Key": "settings-delegate-role"}), &role)
	readerMembership := func(address string) string {
		t.Helper()
		var member struct {
			MembershipID string `json:"membershipId"`
		}
		decodeProcessJSON(t, f.request(ctx, address, http.MethodGet, "/api/v1/me", "local-reader", "", 200), &member)
		if !identity.ValidTenantID(member.MembershipID) {
			t.Fatal("reader has no valid membership ID")
		}
		return member.MembershipID
	}
	memberID := readerMembership(cfg.HTTP.Address)
	var grant identity.AccessAssignment
	decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/assignments", "local-admin", `{"roleDefinitionId":"`+role.ID+`","membershipId":"`+memberID+`","reason":"Owned settings delegate"}`, 201, map[string]string{"Idempotency-Key": "settings-delegate-grant"}), &grant)
	previousSettings, delegateTag := read(peerCfg.HTTP.Address, "local-reader")
	delegateSave := func(address string, status int) settingsExchange {
		t.Helper()
		return exchange(address, "local-reader", http.MethodPost, `{"overrides":{"automaticSourceSyncEnabled":false,"newJobMaxAttempts":9},"reason":"Delegated settings save"}`, "settings-delegate-save", delegateTag, status)
	}
	original := delegateSave(peerCfg.HTTP.Address, 200)
	var receipt tenantsettings.View
	decodeProcessJSON(t, original.body, &receipt)
	if receipt.TenantID != tenant || receipt.Revision != previousSettings.Revision+1 || receipt.Effective.NewJobMaxAttempts != 9 {
		t.Fatal("delegate did not save the expected settings revision")
	}
	latest := save(cfg.HTTP.Address, `{"automaticSourceSyncEnabled":false,"newJobMaxAttempts":4}`)
	visible, latestTag := read(peerCfg.HTTP.Address, "local-reader")
	if latest.Revision != receipt.Revision+1 || latest.Effective.NewJobMaxAttempts != 4 || !bytes.Equal(mustSettingsJSON(t, latest), mustSettingsJSON(t, visible)) {
		t.Fatal("delegate could not read the administrator's newer settings")
	}
	replay := delegateSave(cfg.HTTP.Address, 200)
	if !bytes.Equal(original.body, replay.body) || replay.headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("cross-API exact settings replay changed")
	}
	f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/access/assignments/"+grant.ID+":revoke", "local-admin", `{"reason":"Revoke settings delegate"}`, 200, map[string]string{"If-Match": fmt.Sprintf(`"rev-%d"`, grant.Revision)})
	for _, address := range []string{cfg.HTTP.Address, peerCfg.HTTP.Address} {
		requireSettingsForbidden(t, exchange(address, "local-reader", http.MethodGet, "", "", "", 403), tenant)
		requireSettingsForbidden(t, delegateSave(address, 403), tenant)
	}
	if readerMembership(peerCfg.HTTP.Address) != memberID {
		t.Fatal("revoking the settings assignment changed the reader's membership")
	}
	var assignments identity.AuthorizationPage[identity.AccessAssignment]
	decodeProcessJSON(t, f.request(ctx, peerCfg.HTTP.Address, "GET", "/api/v1/access/assignments?membershipId="+memberID+"&roleDefinitionId="+role.ID, "local-admin", "", 200), &assignments)
	if len(assignments.Items) != 1 || assignments.NextCursor != "" {
		t.Fatal("expected exactly one retained settings delegation")
	}
	revoked := assignments.Items[0]
	if revoked.ID != grant.ID || revoked.MembershipID != memberID || revoked.RoleDefinitionID != role.ID || revoked.State != identity.AssignmentRevoked || revoked.Revision != grant.Revision+1 || revoked.RevokedAt == nil {
		t.Fatal("revocation did not persist on the delegate's assignment")
	}
	current, oldTag := read(cfg.HTTP.Address, "local-admin")
	if oldTag != latestTag || !bytes.Equal(mustSettingsJSON(t, current), mustSettingsJSON(t, latest)) {
		t.Fatal("replaying a historical save or revoking access changed current settings")
	}
	var changes, delegatedChanges int64
	if err := owner.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE actor=$2 AND request_id=$3 AND target_type='tenant_settings' AND target_id=tenant_id::text)
		FROM audit_events WHERE tenant_id=$1 AND action='tenant.settings.changed'`, tenant, memberID, original.headers.Get("X-Request-ID")).Scan(&changes, &delegatedChanges); err != nil || changes != current.Revision || delegatedChanges != 1 {
		t.Fatalf("settings change audits: total=%d want=%d, original delegated save=%d want=1, error=%v", changes, current.Revision, delegatedChanges, err)
	}
	assertIdentities()
	// A replacement changes only inherited inputs. Overrides survive, and old
	// API-observed validators cannot authorize fresh commands against new defaults.
	stopAPI()
	changed := cfg
	changed.Jobs.MaxAttempts = 7
	changed.Worker.SourceMaintenanceInterval.Duration = 2 * time.Minute
	stopAPI = f.start(ctx, "api", apiDSN, f.configuration("settings-changed-defaults", changed), cfg.HTTP.Address)
	inherited, _ := read(cfg.HTTP.Address, "local-admin")
	if inherited.Revision != current.Revision || inherited.Effective.NewJobMaxAttempts != 4 || inherited.DeploymentDefaults.NewJobMaxAttempts != 7 {
		t.Fatal("restart/default change overwrote policy")
	}
	exchange(cfg.HTTP.Address, "local-admin", http.MethodPost, `{"overrides":{},"reason":"Stale API fallback"}`, "settings-old-default", oldTag, 412)
	reset := save(cfg.HTTP.Address, `{}`)
	peerReset, _ := read(peerCfg.HTTP.Address, "local-admin")
	if reset.Effective.NewJobMaxAttempts != 7 || peerReset.Effective.NewJobMaxAttempts != cfg.Jobs.MaxAttempts || peerReset.Revision != reset.Revision {
		t.Fatal("reset inheritance or API-specific labeling")
	}
	inheritedID := manual(first, cfg.HTTP.Address, "settings-inherited-seven", 202)
	if inheritedID == manualID {
		t.Fatal("new manual admission reused a terminal Job")
	}
	awaitJob(first, "manual", 7, inheritedID)
	// A real worker policy-read outage must not publish a default-budget Job.
	save(cfg.HTTP.Address, `{"automaticSourceSyncInterval":"1m","newJobMaxAttempts":2}`)
	if _, err := owner.Exec(ctx, `ALTER TABLE tenant_settings RENAME TO unavailable_tenant_settings`); err != nil {
		t.Fatal(err)
	}
	var third federation.Source
	func() {
		defer func() {
			if _, err := owner.Exec(ctx, `ALTER TABLE unavailable_tenant_settings RENAME TO tenant_settings`); err != nil {
				t.Fatal(err)
			}
		}()
		third = source("local-admin", "worker-read-outage", "Worker read outage")
		await("worker policy failure", func() bool {
			for _, worker := range workers {
				var status struct {
					Components map[string]string `json:"components"`
				}
				decodeProcessJSON(t, f.request(ctx, worker.Worker.OperationsAddress, "GET", "/status", "", "", 200), &status)
				if status.Components["source_maintainer"] == "degraded" {
					if countJobs(third) != 0 {
						t.Fatal("worker policy failure admitted defaults")
					}
					return true
				}
			}
			return false
		})
	}()
	awaitJob(third, "automatic", 2, "")
	for i, worker := range workers {
		if processIdentity(worker.Worker.OperationsAddress, "/status") != identities[i+2] {
			t.Fatal("policy recovery restarted a worker")
		}
	}
	// Restart both worker consumers with persisted overrides, not a new policy.
	for i := range stops {
		stops[i]()
		stops[i] = f.start(ctx, "worker", workerDSN, f.configuration(fmt.Sprintf("settings-worker-restarted-%d", i), workers[i]), workers[i].Worker.OperationsAddress)
	}
	fourth := source("local-admin", "worker-restarted", "Worker restarted")
	awaitJob(fourth, "automatic", 2, "")
	final := save(cfg.HTTP.Address, `{}`)
	if final.Revision <= reset.Revision {
		t.Fatal("reset lost monotonic history")
	}
	t.Log("tenant settings: two APIs/two workers, current admission, isolated tenants, concurrent edits, fenced replay, policy outage/recovery, retained budgets and restart/default/reset controls passed")
	stopAPI()
	return f.start(ctx, "api", apiDSN, f.configuration("settings-original-defaults", cfg), cfg.HTTP.Address)
}

func requireSettingsForbidden(t *testing.T, response settingsExchange, tenant string) {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	decodeProcessJSON(t, response.body, &problem)
	if problem.Code != "FORBIDDEN" || response.headers.Get("ETag") != "" || response.headers.Get("Idempotency-Replayed") != "" {
		t.Fatal("expected a permission refusal without settings or replay headers")
	}
	for _, private := range []string{tenant, `"overrides"`, `"effective"`, `"revision"`, `"deploymentDefaults"`} {
		if bytes.Contains(response.body, []byte(private)) {
			t.Fatalf("permission refusal disclosed %q", private)
		}
	}
}

func mustSettingsJSON(t *testing.T, value tenantsettings.View) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
