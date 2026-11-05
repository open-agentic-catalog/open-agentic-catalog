//go:build minikube

// Current authority is checked on explicitly selected API Pods, never inferred
// from which UI Pod forwarded a request through its shared upstream Service.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// verifyReplicaAuthority mirrors the scoped-reader Resource grant lifecycle in
// the packaged functional journey, adding explicit cross-process observations.
func (h *harness) verifyReplicaAuthority(admin, reader replicaRequest, namespaceID, key string) error {
	me, err := reader(0, "GET", "/api/v1/me", "", "", nil)
	var access struct {
		MembershipID string `json:"membershipId"`
		Subject      string `json:"subject"`
	}
	if err != nil || me.Status != http.StatusOK || json.Unmarshal(me.Body, &access) != nil || access.MembershipID == "" || access.Subject != "oidc-reader" {
		return errors.New("replica reader membership unavailable")
	}
	body := map[string]string{"namespaceId": namespaceID, "kind": "skill", "canonicalIdentifier": "urn:air:catalog.example.com:replicas:" + key}
	createKey := key + "-create"
	denied, err := reader(1, "POST", "/api/v1/resources", key+"-denied", "", body)
	if err != nil || !replicaProblem(denied, http.StatusForbidden, catalog.CodeForbidden) {
		return errors.New("ungranted reader created a Resource")
	}
	grantBody := map[string]string{"subject": "oidc-reader", "actorType": "human", "role": "contributor", "namespaceId": namespaceID, "reason": "Owned replica authority qualification"}
	var resource catalog.Resource
	var created httpResult
	for stage := range 2 {
		grant, err := admin(stage, "POST", "/api/v1/role-assignments", key+[]string{"-grant", "-restore"}[stage], "", grantBody)
		var assignment identity.RoleAssignment
		if err != nil || grant.Status != http.StatusCreated || grant.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(grant.Body, &assignment) != nil || assignment.ID == "" || assignment.TenantID != primaryTenantID || assignment.MembershipID != access.MembershipID || assignment.Subject != "oidc-reader" || assignment.Role != identity.RoleContributor || assignment.NamespaceID != namespaceID || assignment.State != identity.AssignmentActive {
			return errors.New("scoped grant did not retain the same reader membership")
		}
		if stage == 0 {
			created, err = reader(0, "POST", "/api/v1/resources", createKey, "", body)
			if err != nil || created.Status != http.StatusCreated || created.Header.Get("ETag") != `"rev-1"` || created.Header.Get("Idempotency-Replayed") != "" || json.Unmarshal(created.Body, &resource) != nil || resource.ID == "" || resource.TenantID != primaryTenantID || resource.NamespaceID != namespaceID || resource.Kind != catalog.KindSkill || resource.CanonicalIdentifier != body["canonicalIdentifier"] || resource.CreatedBy != "oidc-reader" || resource.Revision != 1 || resource.DeletedAt != nil {
				return errors.New("scoped reader did not create the requested Resource")
			}
		}
		for offset := 1; offset <= 3; offset++ {
			replay, err := reader(offset%3, "POST", "/api/v1/resources", createKey, "", body)
			if err != nil || replay.Status != http.StatusCreated || replay.Header.Get("ETag") != `"rev-1"` || replay.Header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.Body, created.Body) {
				return errors.New("current scoped authority did not replay retained Resource")
			}
			actual, err := admin(offset%3, "GET", "/api/v1/resources/"+resource.ID, "", "", nil)
			var value catalog.Resource
			if err != nil || actual.Status != http.StatusOK || actual.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(actual.Body, &value) != nil || value.ID != resource.ID || value.TenantID != resource.TenantID || value.NamespaceID != resource.NamespaceID || value.Kind != resource.Kind || value.CanonicalIdentifier != resource.CanonicalIdentifier || value.CreatedBy != resource.CreatedBy || value.Revision != 1 || value.DeletedAt != nil {
				return errors.New("authority changes altered retained Resource")
			}
		}
		if err := h.replicaAuditOnce(admin, resource.ID, "resource", "resource.created"); err != nil {
			return err
		}
		revoked, err := admin((stage+2)%3, "POST", "/api/v1/role-assignments/"+assignment.ID+":revoke", key+[]string{"-revoke", "-finish"}[stage], grant.Header.Get("ETag"), map[string]string{"reason": "Finish owned scoped authority"})
		var revokedAssignment identity.RoleAssignment
		if err != nil || revoked.Status != http.StatusOK || revoked.Header.Get("ETag") != `"rev-2"` || json.Unmarshal(revoked.Body, &revokedAssignment) != nil || revokedAssignment.ID != assignment.ID || revokedAssignment.MembershipID != access.MembershipID || revokedAssignment.State != identity.AssignmentRevoked {
			return errors.New("scoped authority was not revoked")
		}
		for index := range 3 {
			denied, err := reader(index, "POST", "/api/v1/resources", createKey, "", body)
			if err != nil || !replicaProblem(denied, http.StatusForbidden, catalog.CodeForbidden) {
				return errors.New("revoked authority disclosed a cached Resource response")
			}
		}
		if err := h.replicaAuditOnce(admin, resource.ID, "resource", "resource.created"); err != nil {
			return err
		}
	}
	deleted, err := admin(2, "POST", "/api/v1/resources/"+resource.ID+":delete", key+"-delete", `"rev-1"`, map[string]string{"reason": "Remove replica authority fixture"})
	if err != nil || deleted.Status != http.StatusNoContent {
		return errors.New("replica authority fixture cleanup failed")
	}
	for index := range 3 {
		result, err := admin(index, "GET", "/api/v1/resources/"+resource.ID, "", "", nil)
		if err != nil || !replicaProblem(result, http.StatusNotFound, catalog.CodeNotFound) {
			return errors.New("deleted replica authority fixture remains visible")
		}
	}
	return nil
}
