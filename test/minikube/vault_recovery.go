//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (h *harness) vaultReady() error {
	_, err := h.kubectl("wait for recovered Vault", "-n", vaultNamespace, "wait", "pod/"+vaultRelease+"-0", "--for=condition=Ready", "--timeout=90s")
	return err
}
func (h *harness) unsealVault() error {
	if _, err := h.kubectlInput("restore protected owned bootstrap token", []byte(h.vault.rootToken), "-n", vaultNamespace, "exec", "-i", vaultRelease+"-0", "--", "sh", "-ec", "umask 077; cat > /tmp/catalog-vault-bootstrap"); err != nil {
		return err
	}
	if _, err := h.vaultExec("unseal existing owned Vault", map[string]string{"key": h.vault.unsealKey}, "write", "-format=json", "sys/unseal", "-"); err != nil {
		return err
	}
	return h.vaultReady()
}
func (h *harness) vaultPVC() (string, error) {
	raw, err := h.kubectl("observe Vault volume binding", "-n", vaultNamespace, "get", "pod", vaultRelease+"-0", "-o", "json")
	if err != nil {
		return "", err
	}
	var pod struct {
		Spec struct {
			Volumes []struct {
				Claim *struct {
					Name string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &pod) != nil {
		return "", errors.New("invalid Vault volume binding")
	}
	claim := ""
	for _, volume := range pod.Spec.Volumes {
		if volume.Claim != nil {
			if claim != "" {
				return "", errors.New("ambiguous Vault data claims")
			}
			claim = volume.Claim.Name
		}
	}
	if claim == "" {
		return "", errors.New("missing persistent Vault claim")
	}
	raw, err = h.kubectl("observe persistent Vault claim", "-n", vaultNamespace, "get", "pvc", claim, "-o", "json")
	if err != nil {
		return "", err
	}
	var claimObject struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &claimObject) != nil || claimObject.Metadata.UID == "" {
		return "", errors.New("invalid owned persistent Vault claim")
	}
	return claimObject.Metadata.UID, nil
}
func (h *harness) verifyVaultProviderRecovery(baseURL string, before []podIdentity) error {
	if _, err := h.vaultExecWithTimeout(h.ctx, 30*time.Second, "seal owned Vault", nil, "operator", "seal"); err != nil {
		return err
	}
	if _, err := h.kubectl("observe sealed server readiness", "-n", vaultNamespace, "wait", "pod/"+vaultRelease+"-0", "--for=condition=Ready=false", "--timeout=60s"); err != nil {
		return err
	}
	values, err := h.writeHelmValues("vault-sealed-refusal", "catalog_api")
	if err != nil {
		return err
	}
	if err := h.deployCatalog(values, "4m"); err == nil || !strings.Contains(err.Error(), "freshness is unconfirmed") {
		return errors.New("sealed provider did not refuse maintenance before drain")
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	if err := h.vaultRetainedSnapshot(baseURL); err != nil {
		return err
	}
	if err := h.vaultSnapshot(baseURL, "sealed-provider"); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	if err := h.unsealVault(); err != nil {
		return err
	}
	if err := h.waitVaultInputs(); err != nil {
		return err
	}
	claim, err := h.vaultPVC()
	if err != nil {
		return err
	}
	if _, err := h.kubectl("replace owned Vault Pod", "-n", vaultNamespace, "delete", "pod", vaultRelease+"-0", "--wait=true", "--timeout=60s"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for replacement Vault process", "-n", vaultNamespace, "wait", "pod/"+vaultRelease+"-0", "--for=jsonpath={.status.phase}=Running", "--timeout=90s"); err != nil {
		return err
	}
	if err := h.unsealVault(); err != nil {
		return err
	}
	after, err := h.vaultPVC()
	if err != nil || claim != after {
		return errors.New("vault replacement did not retain its Raft claim")
	}
	if err := h.requireFreshVaultInputs(vaultStartupInputs...); err != nil {
		return err
	}
	if err := h.vaultSourceTest(baseURL, "server-recovered", h.report.Vault.CredentialRevision); err != nil {
		return err
	}
	if err := h.vaultSnapshot(baseURL, "server-recovered"); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "vault-seal-refuses-maintenance-before-drain", "vault-outage-preserves-api-worker-and-complete-snapshots", "vault-pod-replacement-retains-raft-and-recovers-after-unseal")
	return nil
}
func (h *harness) verifyVaultAuthenticationRecovery(baseURL string, before []podIdentity) error {
	object := kubernetesObject{"apiVersion": "secrets.hashicorp.com/v1beta1", "kind": "VaultAuth", "metadata": map[string]string{"name": "catalog-vault-denied", "namespace": namespace}, "spec": map[string]any{"vaultConnectionRef": vaultRelease, "method": "kubernetes", "mount": "kubernetes", "kubernetes": map[string]any{"role": "catalog-not-configured", "serviceAccount": "catalog-vault-runtime", "audiences": []string{"vault"}}}}
	if err := h.applyObject("select deliberately invalid source authentication", object); err != nil {
		return err
	}
	source := vaultRuntimeSecret(vaultSourceSecret, h.vault.expected[vaultSourceSecret])
	spec, ok := source["spec"].(map[string]any)
	if !ok {
		return errors.New("invalid source fixture spec")
	}
	spec["vaultAuthRef"] = "catalog-vault-denied"
	if err := h.applyObject("isolate source authentication failure", source); err != nil {
		return err
	}
	if err := h.vaultWriteGeneration(vaultSourceSecret, h.vault.expected[vaultSourceSecret]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	if err := waitTopology(ctx, func() (bool, error) {
		raw, err := h.vaultRead(ctx, "vaultstaticsecret", vaultSourceSecret)
		if err != nil {
			return false, err
		}
		return strings.Contains(string(raw), "Code: 400") && strings.Contains(string(raw), "invalid role") || strings.Contains(string(raw), "Code: 403"), nil
	}); err != nil {
		return err
	}
	ready, err := h.vaultInputReady(ctx, vaultSourceSecret)
	if err != nil || ready {
		return errors.New("failed source authentication falsely passed fresh input gate")
	}
	if err := h.requireFreshVaultInputs("catalog-migration-database"); err != nil {
		return err
	}
	if err := h.requireFreshVaultInputs(vaultStartupInputs...); err != nil {
		return err
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	if err := h.vaultSourceTest(baseURL, "auth-unavailable", h.report.Vault.CredentialRevision); err != nil {
		return err
	}
	if err := h.vaultRetainedSnapshot(baseURL); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	if err := h.applyObject("restore exact source authentication", vaultRuntimeSecret(vaultSourceSecret, h.vault.expected[vaultSourceSecret])); err != nil {
		return err
	}
	if err := h.waitVaultInputs(); err != nil {
		return err
	}
	for _, pod := range before {
		if err := h.attachVaultMetadataProbe(pod.Name); err != nil {
			return err
		}
	}
	revision := fmt.Sprintf("%s-kv-%d", h.suffix, h.vault.versions[vaultSourceSecret])
	if err := h.waitVaultMountedRevision(before, revision); err != nil {
		return err
	}
	if err := h.vaultSourceTest(baseURL, "auth-repaired", revision); err != nil {
		return err
	}
	if err := h.vaultSnapshot(baseURL, "auth-repaired"); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "vault-authentication-outage-isolates-source-input", "vault-owner-and-startup-gates-exclude-source-credentials", "vault-authentication-repair-activates-complete-successor-without-restart")
	return nil
}
