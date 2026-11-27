//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const upgradeTargetVersion = "minikube-upgrade-target"

// The focused Helm drill crosses a real image boundary while retaining the same schema.
func (h *harness) prepareUpgradeTarget() error {
	h.upgradeTarget = "open-agentic-catalog:minikube-upgrade-target-" + h.suffix
	args := agntcyBuildArguments(h.upgradeTarget, map[string]string{"version": upgradeTargetVersion, "commit": "local", "sourceState": "unknown"})
	if _, err := h.run("build distinct upgrade target", h.options.docker, args...); err != nil {
		return err
	}
	raw, err := h.run("record upgrade target image", h.options.docker, "image", "inspect", "--format", "{{.Id}}", h.upgradeTarget)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" || id == h.report.Images["application"] {
		return errors.New("upgrade requires distinct initial and target images")
	}
	h.report.Images["upgrade-target"] = id
	_, err = h.minikube("load upgrade target into owned nodes", "image", "load", "--daemon=true", "--overwrite=true", h.upgradeTarget)
	return err
}

func (h *harness) deploymentChart() string {
	if h.coordinatorChart != "" {
		return h.coordinatorChart
	}
	return filepath.Join(h.options.repositoryRoot, "deployments/helm/open-agentic-catalog")
}

// deployCatalog invokes the same operator entry point as make helm-deploy.
func (h *harness) deployCatalog(values, timeout string) error {
	if h.vault != nil {
		if err := h.requireFreshVaultInputs(vaultStartupInputs...); err != nil {
			return err
		}
	}
	executable := h.deploymentTool
	if executable == "" {
		executable = filepath.Join(h.temporary, "catalog-deploy")
	}
	_, err := h.run("coordinate selected Catalog deployment", executable,
		"--context", h.profile, "--namespace", h.catalogNamespace(), "--release", releaseName,
		"--chart", h.deploymentChart(),
		"--values", values, "--helm", h.options.helm, "--kubectl", h.options.kubectl, "--timeout", timeout)
	return err
}

func (h *harness) upgradeValues(marker, role string, replicas int, missingSecret bool) (string, error) {
	path, err := h.writeHelmValues(marker, role)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- generated values in the invocation-owned private directory.
	if err != nil {
		return "", err
	}
	var values map[string]json.RawMessage
	if err = json.Unmarshal(raw, &values); err != nil {
		return "", err
	}
	for _, name := range []string{"api", "worker"} {
		var section map[string]any
		if err = json.Unmarshal(values[name], &section); err != nil {
			return "", err
		}
		section["replicas"] = replicas
		values[name], err = json.Marshal(section)
		if err != nil {
			return "", err
		}
	}
	if missingSecret {
		var secrets map[string]any
		if err = json.Unmarshal(values["secrets"], &secrets); err != nil {
			return "", err
		}
		secrets["apiDatabase"] = map[string]string{"name": "deliberately-absent-database", "key": "dsn"}
		values["secrets"], err = json.Marshal(secrets)
		if err != nil {
			return "", err
		}
	}
	raw, err = json.Marshal(values)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, raw, 0o600)
}

// verifyCoordinatedUpgrade qualifies retained-state failure/retry without a
// fictitious predecessor schema or migration. Both builds run the real owner hook.
func (h *harness) verifyCoordinatedUpgrade(runID string, initial []byte) error {
	good, err := h.upgradeValues("coordinated", "catalog_api", 2, false)
	if err != nil {
		return err
	}
	if err = h.deployCatalog(good, "4m"); err != nil {
		return err
	}
	if err = h.verifyUpgradeReadback(runID, initial); err != nil {
		return err
	}
	if err := h.verifyUpgradeBarrier(good); err != nil {
		return err
	}
	if h.options.upgradeOnly {
		if err := h.prepareUpgradeTarget(); err != nil {
			return err
		}
		good, err = h.upgradeValues("upgrade-target", "catalog_api", 2, false)
		if err != nil {
			return err
		}
	}
	if err := h.verifyUpgradeFaults(runID, initial, good); err != nil {
		return err
	}
	// Restore the selected scenario's baseline inputs for its remaining checks.
	baseline, err := h.writeHelmValues("initial", "catalog_api")
	if err != nil {
		return err
	}
	if err = h.deployCatalog(baseline, "4m"); err != nil {
		return err
	}
	if h.options.scenario == "topology" {
		if err = h.verifyUpgradeReadback(runID, initial); err != nil {
			return err
		}
		h.report.Checks = append(h.report.Checks, "coordinated-upgrade-retains-cross-node-replicas")
	}
	return nil
}

// verifyUpgradeFaults injects invalid inputs, then recovers using the caller's exact target.
func (h *harness) verifyUpgradeFaults(runID string, initial []byte, good string) error {
	var originalRows string
	var err error
	if h.options.upgradeOnly {
		originalRows, err = h.upgradeReleaseDigest()
		if err != nil {
			return err
		}
	}
	before := map[string][]podIdentity{}
	for _, role := range []string{"api", "worker"} {
		before[role], err = h.pods(role)
		if err != nil {
			return err
		}
		if err := requireUpgradeReplicas(before[role], h.options.scenario == "topology"); err != nil {
			return fmt.Errorf("%s target replicas: %w", role, err)
		}
		h.recordPlacement("before-coordinated-fault-"+role, before[role])
	}
	invalid, err := h.upgradeValues("invalid-target", "catalog_api", 0, false)
	if err != nil {
		return err
	}
	if h.deployCatalog(invalid, "1m") == nil {
		return errors.New("zero target replicas unexpectedly admitted")
	}
	for _, role := range []string{"api", "worker"} {
		after, err := h.pods(role)
		if err != nil {
			return err
		}
		if !sameUIDs(before[role], after) {
			return errors.New("invalid target disturbed running Pods")
		}
	}
	if h.options.upgradeOnly {
		address, err := h.startPortForward("service/catalog", 8080)
		if err != nil {
			return err
		}
		if err := h.verifyServedBuild("http://"+address, map[string]string{"version": "minikube", "commit": "local"}); err != nil {
			return err
		}
		h.report.Checks = append(h.report.Checks, "original-build-serving-after-target-preflight-refusal")
	}
	badRole, err := h.upgradeValues("invalid-role", "catalog_api_missing", 2, false)
	if err != nil {
		return err
	}
	if h.deployCatalog(badRole, "2m") == nil {
		return errors.New("upgrade unexpectedly accepted missing role")
	}
	if err = h.verifyFailedUpgrade(originalRows); err != nil {
		return err
	}
	raw, err := h.kubectl("inspect retained upgrade hook failure", "-n", h.catalogNamespace(), "get", "job/catalog-migrate", "-o", "jsonpath={.status.failed}")
	if err != nil {
		return err
	}
	if string(raw) != "1" {
		return errors.New("upgrade did not retain the expected failed hook")
	}
	if err = h.deployCatalog(good, "4m"); err != nil {
		return err
	}
	if err = h.verifyUpgradeReadback(runID, initial); err != nil {
		return err
	}
	broken, err := h.upgradeValues("invalid-startup", "catalog_api", 2, true)
	if err != nil {
		return err
	}
	if h.deployCatalog(broken, "1m") == nil {
		return errors.New("missing runtime secret unexpectedly became ready")
	}
	if err = h.verifyFailedUpgrade(originalRows); err != nil {
		return err
	}
	raw, err = h.kubectl("require successful hook cleanup before startup failure", "-n", h.catalogNamespace(), "get", "jobs", "-l", "app.kubernetes.io/component=migration,app.kubernetes.io/instance=catalog", "-o", "json")
	if err != nil {
		return err
	}
	var jobs struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &jobs) != nil || len(jobs.Items) != 0 {
		return errors.New("startup fault did not follow successful migration")
	}
	for range 2 {
		if err = h.deployCatalog(good, "4m"); err != nil {
			return err
		}
		if err = h.verifyUpgradeReadback(runID, initial); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "coordinated-preflight-preserves-running-replicas", "coordinated-migration-failure-stops-both-roles", "coordinated-startup-failure-no-rollback", "coordinated-retry-restores-two-replicas-and-retained-state")
	if h.options.upgradeOnly {
		h.report.Checks = append(h.report.Checks, "stored-release-rows-unchanged-during-both-failures", "recovered-target-serving-on-verified-api-and-worker-images")
	}
	return nil
}

// upgradeReleaseDigest fingerprints every release row for the disposable primary tenant.
func (h *harness) upgradeReleaseDigest() (string, error) {
	const sql = `SELECT encode(sha256(convert_to(string_agg(to_jsonb(r)::text, E'\n' ORDER BY id), 'UTF8')), 'hex')
FROM listing_releases r WHERE tenant_id='` + primaryTenantID + `'`
	raw, err := h.drainSQL(h.ctx, sql)
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(raw))
	if !backupDigestPattern.MatchString(digest) {
		return "", errors.New("missing stored releases in upgrade fixture")
	}
	return digest, nil
}

func (h *harness) verifyFailedUpgrade(originalRows string) error {
	if err := h.requireDrainedRuntimes(); err != nil {
		return err
	}
	if originalRows != "" {
		current, err := h.upgradeReleaseDigest()
		if err != nil {
			return err
		}
		if current != originalRows {
			return errors.New("stored release rows changed during failed upgrade")
		}
		fmt.Printf("[%s] no API or worker Pods; release rows retain SHA-256 %s\n", h.options.scenario, current)
	}
	return nil
}

func (h *harness) requireDrainedRuntimes() error {
	ctx, cancel := context.WithTimeout(h.ctx, time.Minute)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		for _, role := range []string{"api", "worker"} {
			raw, err := h.kubectl("verify drained deployment", "-n", h.catalogNamespace(), "get", "deployment/catalog-"+role, "-o", "jsonpath={.spec.replicas}")
			if err != nil {
				return false, err
			}
			if string(raw) != "0" {
				return false, errors.New("failed deployment restarted runtime replicas")
			}
			raw, err = h.kubectl("require absence including terminating Pods", "-n", h.catalogNamespace(), "get", "pods", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role, "-o", "json")
			if err != nil {
				return false, err
			}
			var pods struct {
				Items []json.RawMessage `json:"items"`
			}
			if json.Unmarshal(raw, &pods) != nil || pods.Items == nil {
				return false, errors.New("invalid runtime Pod inventory")
			}
			if len(pods.Items) > 0 {
				return false, nil
			}
		}
		return true, nil
	})
}

func (h *harness) verifyUpgradeReadback(runID string, initial []byte) error {
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		if err := requireUpgradeReplicas(pods, h.options.scenario == "topology"); err != nil {
			return fmt.Errorf("%s retry lost configured replica intent: %w", role, err)
		}
		if h.upgradeTarget != "" {
			for _, pod := range pods {
				if pod.ImageID != h.report.Images["upgrade-target"] {
					return fmt.Errorf("recovered %s Pod %s has unexpected image ID %s", role, pod.Name, pod.ImageID)
				}
			}
		}
		h.recordPlacement("coordinated-readback-"+role, pods)
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	if err = h.waitHTTP("http://" + address + "/readyz"); err != nil {
		return err
	}
	if h.upgradeTarget != "" {
		if err := h.verifyServedBuild("http://"+address, map[string]string{"version": upgradeTargetVersion, "commit": "local"}); err != nil {
			return err
		}
	}
	replayed, err := h.runSmoke("http://"+address, runID)
	if err != nil {
		return err
	}
	if !sameSmokeState(initial, replayed) {
		return errors.New("coordinated upgrade changed retained governed state")
	}
	return nil
}

// requireUpgradeReplicas rejects a nominal replica count that hides unready or
// co-located workloads in the cross-node upgrade qualification.
func requireUpgradeReplicas(pods []podIdentity, crossNode bool) error {
	if len(pods) != 2 || !pods[0].Ready || !pods[1].Ready {
		return errors.New("expected two ready replicas")
	}
	if crossNode && (pods[0].Node == "" || pods[1].Node == "" || pods[0].Node == pods[1].Node) {
		return errors.New("expected replicas on distinct nodes")
	}
	return nil
}

// verifyUpgradeBarrier observes the real migration hook blocked on the ledger
// while no old runtime Pods exist. The SQL lock is owned only by this fixture.
func (h *harness) verifyUpgradeBarrier(values string) (resultErr error) {
	phase, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	lockContext, stopLock := context.WithCancel(phase)
	lockDone := make(chan struct{})
	go func() {
		_, _ = h.drainSQL(lockContext, "SET application_name='n94-migration-barrier'; BEGIN; LOCK TABLE catalog_schema_migrations IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(110); ROLLBACK")
		close(lockDone)
	}()
	defer func() {
		cleanup, finish := context.WithTimeout(context.WithoutCancel(h.ctx), 10*time.Second)
		defer finish()
		_, err := h.drainSQL(cleanup, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='n94-migration-barrier' AND pid<>pg_backend_pid()")
		resultErr = errors.Join(resultErr, err)
		stopLock()
		select {
		case <-lockDone:
		case <-cleanup.Done():
			resultErr = errors.Join(resultErr, errors.New("migration barrier child not reaped"))
		}
	}()
	if err := waitTopology(phase, func() (bool, error) {
		raw, err := h.drainSQL(phase, "SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.application_name='n94-migration-barrier' AND l.relation='catalog_schema_migrations'::regclass AND l.mode='AccessExclusiveLock' AND l.granted")
		return strings.TrimSpace(string(raw)) == "1", err
	}); err != nil {
		return err
	}
	deployment, stopDeployment := context.WithCancel(phase)
	completed := make(chan error, 1)
	deploymentWaited := false
	go func() {
		_, err := h.runWithInput(deployment, "deploy behind controlled ledger barrier", nil, filepath.Join(h.temporary, "catalog-deploy"),
			"--context", h.profile, "--namespace", h.catalogNamespace(), "--release", releaseName, "--chart", h.deploymentChart(),
			"--values", values, "--helm", h.options.helm, "--kubectl", h.options.kubectl, "--timeout", "1m")
		completed <- err
	}()
	defer func() {
		stopDeployment()
		if deploymentWaited {
			return
		}
		select {
		case err := <-completed:
			resultErr = errors.Join(resultErr, err)
		case <-time.After(70 * time.Second):
			resultErr = errors.Join(resultErr, errors.New("deployment child not reaped"))
		}
	}()
	if err := waitTopology(phase, func() (bool, error) {
		raw, err := h.drainSQL(phase, "SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.relation='catalog_schema_migrations'::regclass AND NOT l.granted AND a.usename='catalog_owner'")
		return strings.TrimSpace(string(raw)) == "1", err
	}); err != nil {
		return err
	}
	if err := h.requireDrainedRuntimes(); err != nil {
		return err
	}
	if _, err := h.drainSQL(phase, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='n94-migration-barrier' AND pid<>pg_backend_pid()"); err != nil {
		return err
	}
	// Wait before deferred cancellation so target startup remains a real success.
	select {
	case err := <-completed:
		deploymentWaited = true
		if err != nil {
			return err
		}
	case <-phase.Done():
		return phase.Err()
	}
	h.report.Checks = append(h.report.Checks, "migration-hook-observed-with-zero-old-api-worker-pods")
	return nil
}
