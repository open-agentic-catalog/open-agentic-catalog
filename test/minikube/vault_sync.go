//go:build minikube

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

func (h *harness) createVaultResources() error {
	objects, err := vaultExampleObjects(namespace, "https://"+vaultRelease+"."+vaultNamespace+".svc:8200")
	if err != nil {
		return err
	}
	for _, object := range objects {
		if err := h.applyObject("apply published Vault synchronization example", object); err != nil {
			return err
		}
	}
	return nil
}

type vaultSyncStatus struct {
	Metadata struct {
		Generation int64  `json:"generation"`
		UID        string `json:"uid"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct {
			Type               string `json:"type"`
			Status             string `json:"status"`
			Reason             string `json:"reason"`
			ObservedGeneration int64  `json:"observedGeneration"`
		} `json:"conditions"`
	} `json:"status"`
}

func currentVaultSync(raw []byte) (string, bool) {
	var state vaultSyncStatus
	if json.Unmarshal(raw, &state) != nil || state.Metadata.UID == "" || state.Metadata.Generation < 1 {
		return "", false
	}
	for _, c := range state.Status.Conditions {
		if c.Type == "SecretSynced" && c.Status == "True" && c.Reason == "Synced" && c.ObservedGeneration == state.Metadata.Generation {
			return state.Metadata.UID, true
		}
	}
	return "", false
}
func vaultExpectedData(name string, data map[string]string, version int) (map[string]string, error) {
	if version < 1 {
		return nil, errors.New("vault KV version must be positive")
	}
	expected := map[string]string{"vault-kv-version": fmt.Sprint(version)}
	if name == vaultSourceSecret {
		metadata, err := json.Marshal(struct {
			SchemaVersion int    `json:"schemaVersion"`
			TenantID      string `json:"tenantId"`
			Revision      string `json:"revision"`
			Origin        string `json:"origin"`
			Type          string `json:"type"`
			SecretFile    string `json:"secretFile"`
		}{1, data["tenantId"], fmt.Sprintf("%s-kv-%d", data["epoch"], version), data["origin"], "bearer", "bearer.token"})
		if err != nil {
			return nil, err
		}
		expected["bearer.token"] = data["token"]
		expected["bearer.json"] = string(metadata)
	} else {
		for key, value := range data {
			expected[key] = value
		}
	}
	return expected, nil
}
func (h *harness) vaultRead(ctx context.Context, kind, name string) ([]byte, error) {
	return h.runWithInput(ctx, "observe synchronized "+kind+" "+name, nil, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "--request-timeout=3s", "-n", namespace, "get", kind, name, "--ignore-not-found", "-o", "json")
}
func validateVaultDestination(raw []byte, uid, name string, expected map[string]string) bool {
	var secret struct {
		Data     map[string]string `json:"data"`
		Metadata struct {
			OwnerReferences []struct {
				UID        string `json:"uid"`
				Kind       string `json:"kind"`
				Name       string `json:"name"`
				APIVersion string `json:"apiVersion"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &secret) != nil || len(secret.Data) != len(expected) {
		return false
	}
	owned := false
	for _, owner := range secret.Metadata.OwnerReferences {
		if len(secret.Metadata.OwnerReferences) == 1 && owner.UID == uid && owner.Kind == "VaultStaticSecret" && owner.Name == name && owner.APIVersion == "secrets.hashicorp.com/v1beta1" {
			owned = true
		}
	}
	if !owned {
		return false
	}
	for key, value := range expected {
		decoded, err := base64.StdEncoding.DecodeString(secret.Data[key])
		if err != nil {
			return false
		}
		if key == "bearer.json" {
			var got, want map[string]any
			if json.Unmarshal(decoded, &got) != nil || json.Unmarshal([]byte(value), &want) != nil || !reflect.DeepEqual(got, want) {
				return false
			}
		} else if string(decoded) != value {
			return false
		}
	}
	return true
}
func (h *harness) vaultInputReady(ctx context.Context, name string) (bool, error) {
	data, ok := h.vault.expected[name]
	if !ok {
		return false, errors.New("unknown selected vault input")
	}
	raw, err := h.vaultRead(ctx, "vaultstaticsecret", name)
	if err != nil {
		return false, err
	}
	uid, ready := currentVaultSync(raw)
	if !ready {
		return false, nil
	}
	raw, err = h.vaultRead(ctx, "secret", name)
	if err != nil {
		return false, err
	}
	expected, err := vaultExpectedData(name, data, h.vault.versions[name])
	if err != nil {
		return false, err
	}
	return validateVaultDestination(raw, uid, name, expected), nil
}
func (h *harness) vaultInputsReady(ctx context.Context) (bool, error) {
	if len(h.vault.expected) != 5 {
		return false, errors.New("vault fixture requires exactly five input sets")
	}
	for name := range h.vault.expected {
		ready, err := h.vaultInputReady(ctx, name)
		if err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}

func (h *harness) verifyVaultDatabases() error {
	for _, item := range []struct{ role, password string }{{"catalog_api", h.secrets.apiPassword}, {"catalog_worker", h.secrets.workerPassword}, {"catalog_owner", h.secrets.ownerPassword}} {
		address, err := h.startPortForward("pod/postgres-0", 5432)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
		conn, err := databaseConnection(ctx, address, "postgres", item.role, item.password, h.vault.databaseTLS["ca.pem"], h.vault.databaseTLS["postgres.crt"])
		if err == nil {
			_, err = databaseBackend(ctx, conn, item.role, false)
			_ = conn.Close(ctx)
		}
		cancel()
		if err != nil {
			return fmt.Errorf("fresh verified vault-delivered database identity %s failed: %s", item.role, h.redactDiagnostic(err.Error()))
		}
		if err := h.vaultPasswordDenied(item.role, "not-accepted-"+h.suffix); err != nil {
			return err
		}
		h.report.Vault.DatabaseRoles = append(h.report.Vault.DatabaseRoles, item.role)
	}
	h.report.Checks = append(h.report.Checks, "vault-three-fresh-exact-database-identities-verified-tls")
	return nil
}
