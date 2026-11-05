//go:build minikube

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (h *harness) vaultLogin(serviceNamespace, serviceAccount, audience string) (string, error) {
	return h.vaultLoginContext(h.ctx, serviceNamespace, serviceAccount, audience)
}
func (h *harness) vaultLoginContext(ctx context.Context, serviceNamespace, serviceAccount, audience string) (string, error) {
	input, err := json.Marshal(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest", "spec": map[string]any{"audiences": []string{audience}, "expirationSeconds": 600}})
	if err != nil {
		return "", err
	}
	raw, err := h.topologyCommand(ctx, "request bounded dedicated sync token", input, "create", "--raw", "/api/v1/namespaces/"+serviceNamespace+"/serviceaccounts/"+serviceAccount+"/token", "-f", "-")
	if err != nil {
		return "", err
	}
	var token struct {
		Status struct {
			Token string `json:"token"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &token) != nil || token.Status.Token == "" {
		return "", errors.New("invalid dedicated sync token response")
	}
	h.vault.private = append(h.vault.private, token.Status.Token)
	role := "catalog-runtime"
	if serviceAccount == "catalog-vault-owner" {
		role = "catalog-owner"
	}
	raw, err = h.vaultExecContext(ctx, "authenticate dedicated sync identity", map[string]string{"role": role, "jwt": token.Status.Token}, "write", "-format=json", "auth/kubernetes/login", "-")
	if err != nil {
		return "", err
	}
	var login struct {
		Auth struct {
			Token string `json:"client_token"`
		} `json:"auth"`
	}
	if json.Unmarshal(raw, &login) != nil || login.Auth.Token == "" {
		return "", errors.New("invalid scoped Vault login response")
	}
	h.vault.private = append(h.vault.private, login.Auth.Token)
	return login.Auth.Token, nil
}
func (h *harness) vaultScopedRead(token, path string) ([]byte, error) {
	return h.vaultScopedReadContext(h.ctx, token, path)
}
func (h *harness) vaultScopedReadContext(ctx context.Context, token, path string) ([]byte, error) {
	return h.topologyCommand(ctx, "check exact scoped Vault read", []byte(token+"\n"), "-n", vaultNamespace, "exec", "-i", vaultRelease+"-0", "--", "sh", "-ec", `IFS= read -r VAULT_TOKEN; export VAULT_TOKEN; export VAULT_CLIENT_TIMEOUT=5s; exec vault read -format=json "$1"`, "--", "catalog/data/"+path)
}
func vaultPermissionDenied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Code: 403")
}
func (h *harness) verifyVaultDenials() error {
	for _, entry := range []struct{ account, allowed, denied string }{{"catalog-vault-runtime", "catalog-cursor-signing", "catalog-migration-database"}, {"catalog-vault-owner", "catalog-migration-database", "catalog-api-database"}} {
		token, err := h.vaultLogin(namespace, entry.account, "vault")
		if err != nil {
			return err
		}
		if _, err := h.vaultScopedRead(token, entry.allowed); err != nil {
			return err
		}
		if _, err := h.vaultScopedRead(token, entry.denied); !vaultPermissionDenied(err) {
			return errors.New("scoped Vault policy did not deny the other role's path")
		}
		if _, err := h.vaultScopedRead(token, entry.allowed); err != nil {
			return err
		}
	}
	for _, entry := range []struct{ namespace, account, audience string }{{namespace, "catalog-vault-denied", "vault"}, {vaultNamespace, "catalog-vault-runtime", "vault"}, {namespace, "catalog-vault-runtime", "wrong-audience"}} {
		if entry.audience == "vault" {
			if err := h.applyObject("create denied sync identity", kubernetesObject{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]string{"name": entry.account, "namespace": entry.namespace}, "automountServiceAccountToken": false}); err != nil {
				return err
			}
		}
		_, err := h.vaultLogin(entry.namespace, entry.account, entry.audience)
		if !vaultPermissionDenied(err) && !(entry.audience == "wrong-audience" && err != nil && strings.Contains(err.Error(), "Code: 400") && strings.Contains(err.Error(), "audience")) {
			return errors.New("vault did not reject the exact identity or audience mismatch")
		}
	}
	if _, err := h.vaultLogin(namespace, "catalog-vault-runtime", "vault"); err != nil {
		return err
	}
	if err := h.verifyVaultDestinationConflict(); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "vault-runtime-owner-path-isolation", "vault-serviceaccount-namespace-audience-denials", "vault-existing-destination-ownership-refusal")
	return nil
}
func (h *harness) verifyVaultDestinationConflict() error {
	const name = "catalog-vault-conflict"
	if err := h.createSecret("create unrelated destination control", name, map[string]string{"sentinel": "preserve-owned-fixture"}); err != nil {
		return err
	}
	object := vaultRuntimeSecret(name, map[string]string{"key": ""})
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return errors.New("invalid conflict fixture spec")
	}
	spec["path"] = "catalog-cursor-signing"
	if err := h.applyObject("test refusal to overwrite another Secret", object); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	if err := waitTopology(ctx, func() (bool, error) {
		return h.vaultFailureEvent(ctx, name, "not the owner of the destination Secret")
	}); err != nil {
		return err
	}
	raw, err := h.vaultRead(ctx, "secret", name)
	if err != nil {
		return err
	}
	var destination struct {
		Data     map[string]string `json:"data"`
		Metadata struct {
			Owners []any `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &destination) != nil || len(destination.Data) != 1 || destination.Data["sentinel"] != base64.StdEncoding.EncodeToString([]byte("preserve-owned-fixture")) || len(destination.Metadata.Owners) != 0 {
		return errors.New("conflicting destination data or ownership changed")
	}
	return nil
}

// Destination write errors are events; a preceding successful condition can remain.
func (h *harness) vaultFailureEvent(ctx context.Context, name, message string) (bool, error) {
	raw, err := h.vaultRead(ctx, "vaultstaticsecret", name)
	if err != nil {
		return false, err
	}
	var object struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Metadata.UID == "" {
		return false, errors.New("invalid failed sync object")
	}
	raw, err = h.topologyCommand(ctx, "observe exact synchronization failure", nil, "--request-timeout=3s", "-n", namespace, "get", "events", "--field-selector=involvedObject.uid="+object.Metadata.UID, "-o", "json")
	if err != nil {
		return false, err
	}
	var events struct {
		Items []struct {
			Type    string `json:"type"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &events) != nil {
		return false, errors.New("invalid sync event response")
	}
	for _, event := range events.Items {
		if event.Type == "Warning" && event.Reason == "SecretSyncError" && strings.Contains(event.Message, message) {
			return true, nil
		}
	}
	return false, nil
}
