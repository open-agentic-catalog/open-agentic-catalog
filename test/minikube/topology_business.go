//go:build minikube

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// verifyDisruptionBusiness reads and replays pre-fault evidence, then proves a
// fresh command, exact replay and singular audit on the available topology.
func (h *harness) verifyDisruptionBusiness(baseURL, phase string) error {
	request := func(_ int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, baseURL+path, h.tokens["admin"], key, match, body, nil)
	}
	if h.disruptionEvidence.Key != "" {
		if err := h.verifyTopologyEvidence(request, h.disruptionEvidence); err != nil {
			return err
		}
	}
	key := "disruption-" + phase + "-" + h.suffix
	body := map[string]string{"slug": key, "displayName": "Disruption " + phase}
	response, err := request(0, "POST", "/api/v1/namespaces", key, "", body)
	if err != nil {
		return err
	}
	entry := replicaEvidence{Key: key, Body: body, Want: response}
	if err := h.verifyTopologyEvidence(request, entry); err != nil {
		return err
	}
	h.disruptionEvidence = entry
	h.report.Checks = append(h.report.Checks, "retained-command-replay-audit-"+phase)
	return nil
}

func (h *harness) verifyTopologyEvidence(request replicaRequest, entry replicaEvidence) error {
	var created catalog.Namespace
	if entry.Key == "" || entry.Want.Status != http.StatusCreated || entry.Want.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(entry.Want.Body, &created) != nil || created.ID == "" || created.TenantID != primaryTenantID || created.Revision != 1 || created.Slug != entry.Body["slug"] || created.DisplayName != entry.Body["displayName"] {
		return errors.New("disruption command has invalid retained state")
	}
	read, err := request(0, "GET", "/api/v1/namespaces/"+created.ID, "", "", nil)
	if err != nil || !sameNamespaceState(read, entry.Want) {
		return errors.New("disruption lost or changed retained namespace")
	}
	replay, err := request(0, "POST", "/api/v1/namespaces", entry.Key, "", entry.Body)
	if err != nil || replay.Status != 201 || replay.Header.Get("Idempotency-Replayed") != "true" || replay.Header.Get("ETag") != entry.Want.Header.Get("ETag") || !bytes.Equal(replay.Body, entry.Want.Body) {
		return errors.New("disruption failed exact command replay")
	}
	if err := h.replicaAuditOnce(request, created.ID, "namespace", "namespace.created"); err != nil {
		return fmt.Errorf("disruption audit: %w", err)
	}
	return nil
}
