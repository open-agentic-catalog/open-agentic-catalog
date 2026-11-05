//go:build minikube

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

const restoredPostgres = "postgres-restored"

var backupDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type backupRestoreReport struct {
	Tables            int      `json:"tables"`
	Tenants           int      `json:"tenants"`
	VerificationPages []int    `json:"verificationPages"`
	DumpBytes         int64    `json:"dumpBytes"`
	DumpDigest        string   `json:"dumpDigest"`
	InventoryDigest   string   `json:"inventoryDigest"`
	VolumeUIDs        []string `json:"volumeUids"`
	SourceStopped     bool     `json:"sourceStopped"`
}

type backupTable struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`
}

// backupDatabaseObjects reuses the reviewed PostgreSQL fixture with distinct
// selectors, StatefulSet identity and a separately allocated persistent claim.
func (h *harness) backupDatabaseObjects() ([]kubernetesObject, error) {
	var result []kubernetesObject
	for _, object := range h.dependencyObjects() {
		metadata, ok := object["metadata"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid database fixture metadata")
		}
		if metadata["name"] != "postgres" {
			continue
		}
		metadata["name"], metadata["labels"] = restoredPostgres, testLabels(restoredPostgres)
		spec, ok := object["spec"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid database fixture spec")
		}
		if object["kind"] == "Service" {
			spec["selector"] = testLabels(restoredPostgres)
		} else {
			spec["serviceName"] = restoredPostgres
			spec["selector"] = map[string]any{"matchLabels": testLabels(restoredPostgres)}
			template, ok := spec["template"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid database Pod template")
			}
			labels, ok := template["metadata"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid database Pod metadata")
			}
			labels["labels"] = testLabels(restoredPostgres)
			// The entrypoint's temporary initialization server accepts Unix
			// sockets before creating the requested database. Require TCP.
			pod, ok := template["spec"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid database Pod spec")
			}
			containers, ok := pod["containers"].([]any)
			if !ok || len(containers) != 1 {
				return nil, errors.New("invalid database container inventory")
			}
			container, ok := containers[0].(map[string]any)
			if !ok {
				return nil, errors.New("invalid database container")
			}
			container["readinessProbe"] = map[string]any{"exec": map[string]any{"command": []string{"pg_isready", "--host=127.0.0.1", "-U", "catalog_owner", "-d", "catalog"}}, "periodSeconds": 2, "timeoutSeconds": 2, "failureThreshold": 30}
		}
		result = append(result, object)
	}
	if len(result) != 2 {
		return nil, errors.New("restore needs exactly one Service and StatefulSet")
	}
	return result, nil
}

func (h *harness) backupSQL(pod, statement string) ([]byte, error) {
	return h.kubectlInput("inspect owned backup database", []byte("SET timezone='UTC'; SET statement_timeout='15s';\n"+statement),
		"-n", h.catalogNamespace(), "exec", "-i", pod, "--", "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-f", "-")
}

// backupInventory includes every public table, including immutable and derived
// state. It returns only bounded counts and digests, never retained row values.
func (h *harness) backupInventory(pod string) ([]backupTable, error) {
	const query = `SELECT format($q$SELECT json_build_object('table', %L, 'rows', count(*),
'bytes', coalesce(sum(octet_length(to_jsonb(v)::text)),0),
'digest', encode(sha256(convert_to(coalesce(string_agg(to_jsonb(v)::text,E'\n' ORDER BY to_jsonb(v)::text),''),'UTF8')),'hex'))
FROM (SELECT * FROM public.%I LIMIT 10001) v$q$, tablename, tablename)
FROM pg_tables WHERE schemaname='public' ORDER BY tablename
\gexec
`
	raw, err := h.backupSQL(pod, query)
	if err != nil {
		return nil, err
	}
	return decodeBackupInventory(raw)
}

func decodeBackupInventory(raw []byte) ([]backupTable, error) {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(raw) > maximumCommandOutput || len(lines) < 1 || len(lines) > 150 {
		return nil, errors.New("backup table inventory exceeds its bound")
	}
	result := make([]backupTable, 0, len(lines))
	for _, line := range lines {
		var table backupTable
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil || len(fields) != 4 || fields["table"] == nil || fields["rows"] == nil || fields["bytes"] == nil || fields["digest"] == nil {
			return nil, errors.New("incomplete backup table inventory fields")
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New("null backup table inventory field")
			}
		}
		if err := json.Unmarshal([]byte(line), &table); err != nil || table.Table == "" ||
			table.Rows < 0 || table.Rows > 10000 || table.Bytes < 0 || table.Bytes > 64<<20 ||
			!backupDigestPattern.MatchString(table.Digest) || len(result) > 0 && table.Table <= result[len(result)-1].Table {
			return nil, errors.New("invalid or incomplete backup table inventory")
		}
		result = append(result, table)
	}
	return result, nil
}

// backupControl keeps owner credentials in a Secret and runs the real packaged
// CLI. Each short-lived Job has no retries or runtime listener.
func (h *harness) backupControl(name, secret string, arguments ...string) ([]byte, error) {
	job := h.databaseJob(name, "", []any{
		map[string]any{"name": "CATALOG_DATABASE_DRIVER", "value": "postgres"},
		secretEnvironment("CATALOG_DATABASE_DSN", secret, "dsn"),
	})
	spec, ok := job["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid backup operator Job spec")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid backup operator Pod template")
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid backup operator Pod spec")
	}
	security, ok := pod["securityContext"].(map[string]any)
	if !ok {
		return nil, errors.New("missing backup operator security context")
	}
	security["runAsUser"], security["runAsGroup"] = 65532, 65532
	containers, ok := pod["containers"].([]any)
	if !ok || len(containers) != 1 {
		return nil, errors.New("invalid backup operator container inventory")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return nil, errors.New("invalid backup operator container")
	}
	container["name"], container["image"] = "operator", h.application
	container["command"] = append([]string{"/usr/local/bin/catalogctl"}, arguments...)
	if err := h.completeBackupJob(job); err != nil {
		return nil, err
	}
	return h.kubectl("read bounded restore operator report", "-n", h.catalogNamespace(), "logs", "job/"+name)
}

func (h *harness) completeBackupJob(job kubernetesObject) error {
	name, err := backupJobName(job)
	if err != nil {
		return err
	}
	if err := h.applyObject("create owned backup Job", job); err != nil {
		return err
	}
	_, err = h.kubectl("wait for owned backup Job", "-n", h.catalogNamespace(), "wait", "--for=condition=complete", "job/"+name, "--timeout=2m")
	if err != nil {
		if logs, readErr := h.kubectl("read failed backup Job", "-n", h.catalogNamespace(), "logs", "job/"+name); readErr == nil {
			return fmt.Errorf("backup Job failed: %w: %s", err, h.redactDiagnostic(string(logs)))
		}
	}
	return err
}

func backupJobName(job kubernetesObject) (string, error) {
	metadata, ok := job["metadata"].(map[string]any)
	if !ok {
		return "", errors.New("missing backup Job metadata")
	}
	name, ok := metadata["name"].(string)
	if !ok || name == "" {
		return "", errors.New("missing backup Job name")
	}
	return name, nil
}

// backupArchiveScript never cleans a pre-existing database or sends archive
// bytes to the host. Normal completion removes the archive; cluster cleanup
// removes interrupted Pod storage.
const backupArchiveScript = `umask 077
directory=$(mktemp -d /tmp/catalog-backup.XXXXXXXXXX)
trap 'rm -r -- "$directory"' EXIT
pg_dump --host=postgres --username=catalog_owner --dbname=catalog --format=custom --file="$directory/catalog.dump"
size=$(stat -c %s "$directory/catalog.dump")
test "$size" -gt 0 && test "$size" -le 16777216
pg_restore --host=postgres-restored --username=catalog_owner --dbname=catalog --single-transaction "$directory/catalog.dump"
digest=$(sha256sum "$directory/catalog.dump")
printf '{"dumpBytes":%s,"dumpDigest":"%s"}\n' "$size" "${digest%% *}"
`

func (h *harness) verifyBackupRestore(baseURL string, smoke []byte) error {
	// Capture known retained HTTP values before any backup. Recovery never
	// reruns smoke to recreate records that should already be in the archive.
	var baseline struct {
		Resources []struct {
			ResourceID string `json:"resourceId"`
			ReleaseID  string `json:"releaseId"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(smoke, &baseline); err != nil || len(baseline.Resources) != 3 {
		return errors.New("backup needs populated governed resources")
	}
	reads := make(map[string]httpResult)
	paths := []string{"/.well-known/ard.json"}
	for _, resource := range baseline.Resources {
		paths = append(paths, "/api/v1/resources/"+resource.ResourceID, "/api/v1/catalog/resources/"+resource.ReleaseID)
	}
	for _, path := range paths {
		value, err := h.request(http.MethodGet, baseURL+path, h.tokens["admin"], "", "", nil, nil)
		if err != nil {
			return err
		}
		if value.Status != http.StatusOK {
			return fmt.Errorf("capture retained backup read: status %d", value.Status)
		}
		reads[path] = value
	}
	searchBody := map[string]any{"limit": 100}
	search, err := h.request(http.MethodPost, baseURL+"/api/v1/catalog/search", h.tokens["admin"], "", "", searchBody, nil)
	var searchBefore struct {
		Items []json.RawMessage `json:"items"`
	}
	if err != nil || search.Status != http.StatusOK || json.Unmarshal(search.Body, &searchBefore) != nil || len(searchBefore.Items) != 3 {
		return errors.New("backup needs positive local Search results")
	}
	key := "backup-namespace-" + h.suffix
	body := map[string]string{"slug": key, "displayName": "Retained backup namespace"}
	created, err := h.request(http.MethodPost, baseURL+"/api/v1/namespaces", h.tokens["admin"], key, "", body, nil)
	if err != nil || created.Status != http.StatusCreated || created.Header.Get("ETag") != `"rev-1"` {
		return errors.New("backup command baseline is missing")
	}
	var target struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body, &target); err != nil || target.ID == "" {
		return errors.New("invalid backup namespace baseline")
	}
	if err := h.stopPortForwards(); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		if _, err := h.kubectl("stop source Catalog "+role, "-n", h.catalogNamespace(), "scale", "deployment/catalog-"+role, "--replicas=0"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for old Catalog Pods to stop", "-n", h.catalogNamespace(), "wait", "--for=delete", "pod", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role, "--timeout=2m"); err != nil {
			return err
		}
	}
	before, err := h.backupInventory("postgres-0")
	if err != nil {
		return err
	}
	tenants, err := h.backupSQL("postgres-0", "SELECT json_agg(id ORDER BY id) FROM tenants;")
	if err != nil {
		return fmt.Errorf("read backup tenant inventory: %w", err)
	}
	if !validBackupTenants(tenants) {
		return errors.New("backup needs the complete independent tenant inventory")
	}
	plan, err := h.backupControl("backup-source-plan", "catalog-migration-database", "migrate", "plan")
	if err != nil {
		return err
	}
	schema, err := backupSchemaVersion(plan)
	if err != nil {
		return err
	}
	objects, err := h.backupDatabaseObjects()
	if err != nil {
		return err
	}
	for _, object := range objects {
		if err := h.applyObject("create separate restore database", object); err != nil {
			return err
		}
	}
	if _, err := h.kubectl("wait for fresh restore PostgreSQL", "-n", h.catalogNamespace(), "rollout", "status", "statefulset/"+restoredPostgres, "--timeout=3m"); err != nil {
		return err
	}
	empty, err := h.backupSQL(restoredPostgres+"-0", "SELECT count(*) FROM pg_tables WHERE schemaname='public';")
	if err != nil {
		return fmt.Errorf("inspect empty restore destination: %w", err)
	}
	if strings.TrimSpace(string(empty)) != "0" {
		return errors.New("restore destination is not an empty owned database")
	}
	if err := h.completeBackupJob(h.databaseRoleJob("backup-restore-roles", restoredPostgres)); err != nil {
		return err
	}
	for _, input := range []struct{ name, role, password string }{
		{"migration", "catalog_owner", h.secrets.ownerPassword}, {"api", "catalog_api", h.secrets.apiPassword}, {"worker", "catalog_worker", h.secrets.workerPassword},
	} {
		if err := h.createSecret("create isolated restore connection", "catalog-restored-"+input.name, map[string]string{
			"dsn": fmt.Sprintf("postgres://%s:%s@%s/catalog?sslmode=disable", input.role, input.password, net.JoinHostPort(restoredPostgres, "5432")),
		}); err != nil {
			return err
		}
	}
	if err := h.completeBackupJob(h.databaseJob("backup-logical-round-trip", backupArchiveScript, []any{secretEnvironment("PGPASSWORD", "postgres-owner", "password")})); err != nil {
		return err
	}
	report := &backupRestoreReport{Tables: len(before), Tenants: 2}
	receipt, err := h.kubectl("read backup archive receipt", "-n", h.catalogNamespace(), "logs", "job/backup-logical-round-trip")
	if err != nil || json.Unmarshal(receipt, report) != nil || report.DumpBytes < 1 || report.DumpBytes > 16<<20 || !backupDigestPattern.MatchString(report.DumpDigest) {
		return errors.New("backup archive receipt is missing or invalid")
	}
	if _, err := h.kubectl("stop original PostgreSQL", "-n", h.catalogNamespace(), "scale", "statefulset/postgres", "--replicas=0"); err != nil {
		return err
	}
	if _, err := h.kubectl("require original PostgreSQL to remain stopped", "-n", h.catalogNamespace(), "wait", "--for=delete", "pod/postgres-0", "--timeout=2m"); err != nil {
		return err
	}
	restoredPlan, err := h.backupControl("backup-restored-plan", "catalog-restored-migration", "migrate", "plan")
	if err != nil || !bytes.Equal(plan, restoredPlan) {
		return errors.New("restored migration plan differs from the source")
	}
	applied, err := h.backupControl("backup-reconcile", "catalog-restored-migration", "migrate", "apply", "--api-role", "catalog_api", "--worker-role", "catalog_worker")
	if err != nil {
		return err
	}
	var privileges struct {
		Applied           []json.RawMessage `json:"applied"`
		RuntimePrivileges []struct {
			Compliant       bool `json:"compliant"`
			ManifestVersion int  `json:"manifestVersion"`
		} `json:"runtimePrivileges"`
	}
	if err := json.Unmarshal(applied, &privileges); err != nil || len(privileges.Applied) != 0 || len(privileges.RuntimePrivileges) != 2 {
		return errors.New("restore unexpectedly needs migrations or lacks exact privilege reports")
	}
	for _, role := range privileges.RuntimePrivileges {
		if !role.Compliant || role.ManifestVersion != database.RuntimePrivilegeManifestVersion() {
			return errors.New("restored runtime privileges are not compliant")
		}
	}
	for index, tenant := range []string{primaryTenantID, otherTenantID} {
		pages, err := h.verifyBackupTenant(index, tenant, schema)
		if err != nil {
			return err
		}
		report.VerificationPages = append(report.VerificationPages, pages)
	}
	after, err := h.backupInventory(restoredPostgres + "-0")
	if err != nil || !slices.Equal(before, after) {
		return errors.New("restored table counts or digests differ from the source")
	}
	inventory, err := json.Marshal(before)
	if err != nil {
		return err
	}
	report.InventoryDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(inventory))
	if err := h.verifyBackupVolumes(report); err != nil {
		return err
	}
	values, err := h.writeHelmValues("restored", "catalog_api")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(values) // #nosec G304 -- invocation-owned Helm values.
	if err != nil {
		return err
	}
	var selected map[string]any
	if err := json.Unmarshal(raw, &selected); err != nil {
		return err
	}
	secrets, ok := selected["secrets"].(map[string]any)
	if !ok {
		return errors.New("missing restore Helm Secret references")
	}
	for _, role := range []string{"api", "worker", "migration"} {
		secrets[role+"Database"] = map[string]string{"name": "catalog-restored-" + role, "key": "dsn"}
	}
	raw, err = json.Marshal(selected)
	if err != nil {
		return err
	}
	if err := os.WriteFile(values, raw, 0o600); err != nil {
		return err
	}
	if err := h.deployCatalog(values, "4m"); err != nil {
		return err
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL = "http://" + address
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	for path, expected := range reads {
		actual, err := h.request(http.MethodGet, baseURL+path, h.tokens["admin"], "", "", nil, nil)
		if err != nil || actual.Status != expected.Status || !bytes.Equal(actual.Body, expected.Body) || actual.Header.Get("ETag") != expected.Header.Get("ETag") {
			return errors.New("restored Catalog read or validator changed")
		}
	}
	search, err = h.request(http.MethodPost, baseURL+"/api/v1/catalog/search", h.tokens["admin"], "", "", searchBody, nil)
	var searchAfter struct {
		Items []json.RawMessage `json:"items"`
	}
	if err != nil || search.Status != http.StatusOK || json.Unmarshal(search.Body, &searchAfter) != nil || !slices.EqualFunc(searchBefore.Items, searchAfter.Items, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		return errors.New("restored local Search results changed")
	}
	replay, err := h.request(http.MethodPost, baseURL+"/api/v1/namespaces", h.tokens["admin"], key, "", body, nil)
	if err != nil || !sameBackupReplay(created, replay) {
		return errors.New("restored completed command did not replay exactly")
	}
	denied, err := h.request(http.MethodGet, baseURL+"/api/v1/namespaces/"+target.ID, h.tokens["other"], "", "", nil, nil)
	if err != nil || denied.Status != http.StatusNotFound {
		return errors.New("restored namespace crossed the tenant boundary")
	}
	stopped, err := h.kubectl("confirm original database is still stopped", "-n", h.catalogNamespace(), "get", "statefulset/postgres", "-o", "jsonpath={.spec.replicas}")
	if err != nil || string(stopped) != "0" {
		return errors.New("original database is no longer fenced")
	}
	report.SourceStopped = true
	h.report.BackupRestore = report
	h.report.Checks = append(h.report.Checks, "logical-backup-into-separate-postgresql-volume", "all-tenant-restore-verification-and-table-digests", "restored-helm-reads-exact-replay-and-tenant-denial")
	return nil
}

func sameBackupReplay(expected, actual httpResult) bool {
	return expected.Status == http.StatusCreated && actual.Status == expected.Status && bytes.Equal(actual.Body, expected.Body) &&
		expected.Header.Get("ETag") == `"rev-1"` && actual.Header.Get("ETag") == expected.Header.Get("ETag") && actual.Header.Get("Idempotency-Replayed") == "true"
}

func validBackupTenants(raw []byte) bool {
	var tenants []string
	return json.Unmarshal(raw, &tenants) == nil && slices.Equal(tenants, []string{primaryTenantID, otherTenantID})
}

func backupSchemaVersion(raw []byte) (int64, error) {
	var plan []struct {
		Version int64 `json:"version"`
		Applied bool  `json:"applied"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan) == 0 {
		return 0, errors.New("missing backup migration plan")
	}
	var previous int64
	for _, row := range plan {
		if !row.Applied || row.Version <= previous {
			return 0, errors.New("backup requires a complete applied migration plan")
		}
		previous = row.Version
	}
	return previous, nil
}

func acceptBackupPage(raw []byte, tenant, point string, schema int64) (string, error) {
	var page database.RestoreVerificationReport
	if err := json.Unmarshal(raw, &page); err != nil || page.TenantID != tenant || page.RecoveryPoint != point ||
		page.FormatVersion != database.RestoreVerificationFormatVersion || page.CheckSetVersion != database.RestoreVerificationCheckSetVersion ||
		page.PrivilegeManifestVersion != database.RuntimePrivilegeManifestVersion() || page.SchemaVersion != schema ||
		page.Integrity != "valid" || page.Processed < 0 || page.Processed > 100 || page.DecodedBytes < 0 || page.DecodedBytes > database.MaxRestoreVerificationDecodedBytes ||
		page.Findings == nil || len(page.Findings) > database.MaxRestoreVerificationFindings || page.AssuranceBoundary == "" || len(page.Continuation) > database.MaxRestoreVerificationCursorBytes ||
		(page.Traversal != "continued" && page.Traversal != "complete") || (page.Traversal == "continued") != (page.Continuation != "") {
		return "", errors.New("invalid restore verification page")
	}
	for _, finding := range page.Findings {
		domain := "outbox_event"
		if finding.Code == "RESTORE_ACTIONABLE_JOB_RETAINED" {
			domain = "job"
		}
		if finding.Class != "operational" || finding.Severity != "warning" || finding.Domain != domain || !slices.Contains([]string{"RESTORE_ACTIONABLE_JOB_RETAINED", "RESTORE_UNACKNOWLEDGED_OUTBOX_EVENT", "RESTORE_PENDING_INTERNAL_OUTBOX_EVENT"}, finding.Code) {
			return "", errors.New("unreviewed restore verification finding")
		}
	}
	return page.Continuation, nil
}

func readBackupPages(tenant, point string, schema int64, read func(string) ([]byte, error)) (int, error) {
	cursor := ""
	seen := make(map[string]bool)
	for page := 1; page <= 100; page++ {
		raw, err := read(cursor)
		if err != nil {
			return 0, err
		}
		cursor, err = acceptBackupPage(raw, tenant, point, schema)
		if err != nil {
			return 0, err
		}
		if cursor == "" {
			return page, nil
		}
		if seen[cursor] {
			return 0, errors.New("restore verification cursor cycle")
		}
		seen[cursor] = true
	}
	return 0, errors.New("restore verification page bound exceeded")
}

func (h *harness) verifyBackupTenant(index int, tenant string, schema int64) (int, error) {
	page := 0
	return readBackupPages(tenant, "minikube-backup-"+h.suffix, schema, func(cursor string) ([]byte, error) {
		page++
		args := []string{"restore", "verify", "--tenant", tenant, "--recovery-point", "minikube-backup-" + h.suffix, "--limit", "100"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		return h.backupControl(fmt.Sprintf("backup-verify-%d-%d", index, page), "catalog-restored-migration", args...)
	})
}

func (h *harness) verifyBackupVolumes(report *backupRestoreReport) error {
	raw, err := h.kubectl("verify distinct backup volumes", "-n", h.catalogNamespace(), "get", "pvc", "data-postgres-0", "data-postgres-restored-0", "-o", "json")
	if err != nil {
		return err
	}
	var claims struct {
		Items []struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
			Spec struct {
				VolumeName string `json:"volumeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || len(claims.Items) != 2 || claims.Items[0].Metadata.UID == "" || claims.Items[1].Metadata.UID == "" ||
		claims.Items[0].Metadata.UID == claims.Items[1].Metadata.UID || claims.Items[0].Spec.VolumeName == "" || claims.Items[0].Spec.VolumeName == claims.Items[1].Spec.VolumeName {
		return errors.New("restore requires separately allocated persistent volumes")
	}
	report.VolumeUIDs = []string{claims.Items[0].Metadata.UID, claims.Items[1].Metadata.UID}
	return nil
}
