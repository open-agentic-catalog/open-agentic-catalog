//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (h *harness) verifyVault(baseURL string, initialSmoke []byte) error {
	if err := h.applyNetworkPolicies(); err != nil {
		return err
	}
	if err := h.verifyVaultDatabases(); err != nil {
		return err
	}
	inspection, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/source-inspections", h.tokens["admin"], "vault-inspect-"+h.suffix, "", map[string]any{"url": "https://federation-fixture:8443/credentials/bearer/manifest.json", "networkProfile": "functional-fixture", "credentialBinding": "bearer"}, nil)
	var inspected struct {
		ID                 string `json:"id"`
		CredentialRevision string `json:"credentialRevision"`
	}
	revision := h.suffix + "-kv-1"
	if err != nil || inspection.Status != 201 || json.Unmarshal(inspection.Body, &inspected) != nil || inspected.ID == "" || inspected.CredentialRevision != revision {
		return errors.New("vault-delivered bearer source inspection failed")
	}
	response, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/sources", h.tokens["admin"], "vault-source-"+h.suffix, "", map[string]any{"inspectionId": inspected.ID, "displayName": "Owned Vault bearer source", "retrievalMode": "indexed", "scope": map[string]any{"kinds": []string{"agent"}}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "credentialBinding": "bearer", "reason": "Verify externally synchronized credentials"}, nil)
	var source struct {
		ID string `json:"id"`
	}
	if err != nil || response.Status != 201 || json.Unmarshal(response.Body, &source) != nil || source.ID == "" {
		return errors.New("vault source creation failed")
	}
	path := "/api/v1/federation/sources/" + source.ID
	tested, err := h.request(http.MethodPost, baseURL+path+":test", h.tokens["admin"], "vault-test-"+h.suffix, response.Header.Get("ETag"), map[string]any{}, nil)
	var testedResult struct {
		Status             string `json:"status"`
		CredentialRevision string `json:"credentialRevision"`
	}
	if err != nil || tested.Status != 200 || json.Unmarshal(tested.Body, &testedResult) != nil || testedResult.Status != "succeeded" || testedResult.CredentialRevision != revision {
		return errors.New("vault source test did not use the synchronized credential revision")
	}
	queued, err := h.request(http.MethodPost, baseURL+path+":sync", h.tokens["admin"], "vault-sync-"+h.suffix, tested.Header.Get("ETag"), map[string]any{}, nil)
	var envelope struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err != nil || queued.Status != 202 || json.Unmarshal(queued.Body, &envelope) != nil || envelope.Job.ID == "" {
		return errors.New("vault source synchronization was not queued")
	}
	job, err := h.waitJobState(baseURL, envelope.Job.ID, "succeeded", time.Minute)
	if err != nil {
		return err
	}
	result, ok := job["result"].(map[string]any)
	if !ok {
		return errors.New("vault source Job has no snapshot result")
	}
	snapshotID, ok := result["snapshotId"].(string)
	if !ok || snapshotID == "" {
		return errors.New("vault source Job has no snapshot identity")
	}
	snapshot, err := h.request(http.MethodGet, baseURL+"/api/v1/federation/snapshots/"+snapshotID, h.tokens["admin"], "", "", nil, nil)
	var saved struct {
		ID                 string `json:"id"`
		SourceID           string `json:"sourceId"`
		State              string `json:"state"`
		CredentialRevision string `json:"credentialRevision"`
		MemberCount        int    `json:"memberCount"`
	}
	if err != nil || snapshot.Status != 200 || json.Unmarshal(snapshot.Body, &saved) != nil || saved.ID != snapshotID || saved.SourceID != source.ID || saved.State != "complete" || saved.CredentialRevision != revision || saved.MemberCount != 1 {
		return errors.New("worker snapshot lost the exact Vault credential generation")
	}
	h.report.Vault.SourceID = source.ID
	h.report.Vault.SnapshotID = snapshotID
	h.report.Vault.CredentialRevision = revision
	h.report.Checks = append(h.report.Checks, "vault-source-inspection-and-test-exact-kv-revision", "vault-worker-source-snapshot-complete")
	return h.verifyVaultLifecycle(baseURL, initialSmoke)
}
