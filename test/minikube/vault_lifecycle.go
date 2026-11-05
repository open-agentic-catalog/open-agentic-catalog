//go:build minikube

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const vaultOperatorDeployment = "catalog-vso-vault-secrets-operator-controller-manager"

var vaultStartupInputs = []string{"catalog-api-database", "catalog-worker-database", "catalog-migration-database", "catalog-cursor-signing"}

func (h *harness) vaultWriteGeneration(name string, data map[string]string) error {
	version := h.vault.versions[name]
	raw, err := h.vaultExec("publish complete successor "+name, map[string]any{"data": data, "options": map[string]int{"cas": version}}, "write", "-format=json", "catalog/data/"+name, "-")
	if err != nil {
		return err
	}
	var result struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Data.Version != version+1 {
		return errors.New("unexpected successor KV version")
	}
	h.vault.expected[name] = data
	h.vault.versions[name] = result.Data.Version
	return nil
}

func (h *harness) vaultFresh(ctx context.Context, names ...string) (bool, error) {
	raw, err := h.topologyCommand(ctx, "check external operator availability", nil, "--request-timeout=3s", "-n", vaultNamespace, "get", "deployment", vaultOperatorDeployment, "-o", "json")
	if err != nil {
		return false, err
	}
	var deployment struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Ready    int   `json:"readyReplicas"`
			Observed int64 `json:"observedGeneration"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &deployment) != nil {
		return false, errors.New("invalid operator availability response")
	}
	if deployment.Spec.Replicas != 1 || deployment.Status.Ready != 1 || deployment.Status.Observed < deployment.Metadata.Generation {
		return false, nil
	}
	for _, name := range names {
		raw, err := h.vaultExecContext(ctx, "confirm upstream KV version "+name, nil, "read", "-format=json", "catalog/metadata/"+name)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
		var metadata struct {
			Data struct {
				Version int `json:"current_version"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &metadata) != nil {
			return false, errors.New("invalid upstream KV metadata")
		}
		if metadata.Data.Version != h.vault.versions[name] {
			return false, nil
		}
		ready, err := h.vaultInputReady(ctx, name)
		if err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}
func (h *harness) requireFreshVaultInputs(names ...string) error {
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Second)
	defer cancel()
	ready, err := h.vaultFresh(ctx, names...)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("selected vault input freshness is unconfirmed")
	}
	return nil
}
func (h *harness) waitVaultInputs() error {
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) { return h.vaultInputsReady(ctx) })
}
func (h *harness) vaultRuntimePods() ([]podIdentity, error) {
	var result []podIdentity
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return nil, err
		}
		if len(pods) != 1 || !pods[0].Ready || pods[0].Restarts != 0 {
			return nil, errors.New("vault scenario requires ready unrestarted runtimes")
		}
		result = append(result, pods...)
	}
	return result, nil
}
func (h *harness) vaultUnchanged(before []podIdentity) error {
	after, err := h.vaultRuntimePods()
	if err != nil {
		return err
	}
	if !sameUIDs(before, after) {
		return errors.New("external secret delivery unexpectedly replaced Catalog runtimes")
	}
	return nil
}
func (h *harness) patchVaultFixtureSecret(name string, data map[string]string) error {
	encoded := map[string]string{}
	for key, value := range data {
		encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	raw, err := json.Marshal(map[string]any{"data": encoded})
	if err != nil {
		return err
	}
	_, err = h.kubectlInput("update owned upstream fixture input "+name, raw, "-n", namespace, "patch", "secret", name, "--type=merge", "--patch-file=/dev/stdin")
	return err
}
func (h *harness) scaleVaultOperator(replicas string) error {
	if _, err := h.kubectl("set external operator replicas", "-n", vaultNamespace, "scale", "deployment/"+vaultOperatorDeployment, "--replicas="+replicas); err != nil {
		return err
	}
	if replicas == "0" {
		_, err := h.kubectl("wait for stopped operator", "-n", vaultNamespace, "wait", "pod", "-l", "app.kubernetes.io/instance=catalog-vso", "--for=delete", "--timeout=90s")
		return err
	}
	_, err := h.kubectl("wait for restored operator", "-n", vaultNamespace, "rollout", "status", "deployment/"+vaultOperatorDeployment, "--timeout=2m")
	return err
}

func (h *harness) verifyVaultLifecycle(baseURL string, initialSmoke []byte) error {
	if err := h.verifyVaultDenials(); err != nil {
		return err
	}
	before, err := h.vaultRuntimePods()
	if err != nil {
		return err
	}
	if err := h.verifyVaultSourceRotation(baseURL, before); err != nil {
		return err
	}
	baseURL, err = h.rotateVaultDatabases()
	if err != nil {
		return err
	}
	before, err = h.vaultRuntimePods()
	if err != nil {
		return err
	}
	if err := h.verifyVaultProviderRecovery(baseURL, before); err != nil {
		return err
	}
	if err := h.verifyVaultAuthenticationRecovery(baseURL, before); err != nil {
		return err
	}
	if err := h.requireFreshVaultInputs(vaultStartupInputs...); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	replayed, err := h.runSmoke(baseURL, "minikube-"+h.suffix)
	if err != nil {
		return err
	}
	if !sameSmokeState(initialSmoke, replayed) {
		return errors.New("secret maintenance changed governed Catalog state")
	}
	h.report.Checks = append(h.report.Checks, "vault-maintenance-retains-governed-content-and-idempotent-replay")
	return nil
}

func (h *harness) vaultSnapshot(baseURL, phase string) error {
	jobID, err := h.databaseRenewalJob(baseURL, h.report.Vault.SourceID, "vault-"+phase)
	if err != nil {
		return err
	}
	result, err := h.waitJobState(baseURL, jobID, "succeeded", time.Minute)
	if err != nil {
		return err
	}
	output, ok := result["result"].(map[string]any)
	if !ok {
		return errors.New("source Job lacks its exact result")
	}
	id, ok := output["snapshotId"].(string)
	if !ok || id == "" {
		return errors.New("source Job lacks its snapshot")
	}
	response, err := h.request("GET", baseURL+"/api/v1/federation/snapshots/"+id, h.tokens["admin"], "", "", nil, nil)
	var snapshot struct {
		Source   string `json:"sourceId"`
		Revision string `json:"credentialRevision"`
		Count    int    `json:"memberCount"`
		State    string `json:"state"`
	}
	revision := fmt.Sprintf("%s-kv-%d", h.suffix, h.vault.versions[vaultSourceSecret])
	if err != nil || response.Status != 200 || json.Unmarshal(response.Body, &snapshot) != nil || snapshot.Source != h.report.Vault.SourceID || snapshot.Revision != revision || snapshot.Count != 1 || snapshot.State != "complete" {
		return errors.New("worker snapshot does not match the active complete source generation")
	}
	h.report.Vault.SnapshotID = id
	h.report.Vault.CredentialRevision = revision
	return nil
}

func (h *harness) vaultRetainedSnapshot(baseURL string) error {
	response, err := h.request("GET", baseURL+"/api/v1/federation/snapshots/"+h.report.Vault.SnapshotID, h.tokens["admin"], "", "", nil, nil)
	var snapshot struct {
		ID       string `json:"id"`
		Source   string `json:"sourceId"`
		Revision string `json:"credentialRevision"`
		Count    int    `json:"memberCount"`
		State    string `json:"state"`
	}
	if err != nil || response.Status != 200 || json.Unmarshal(response.Body, &snapshot) != nil || snapshot.ID != h.report.Vault.SnapshotID || snapshot.Source != h.report.Vault.SourceID || snapshot.Revision != h.report.Vault.CredentialRevision || snapshot.Count != 1 || snapshot.State != "complete" {
		return errors.New("provider outage changed or hid the last complete source snapshot")
	}
	return nil
}
