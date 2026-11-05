//go:build functional

package httpserver

import (
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"testing"
)

func TestGroupArchiveRequiresExplicitCleanup(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	create := func(name string) (identity.AccessGroup, string) {
		r := requestJSON(t, server.URL, "POST", "/api/v1/access/groups", "admin", name, "", map[string]any{"name": name, "reason": "Create owned archive control"})
		requireStatus(t, r, 201)
		var g identity.AccessGroup
		decodeResult(t, r, &g)
		return g, r.header.Get("ETag")
	}
	clean, etag := create("empty-control")
	r := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+clean.ID+":archive", "admin", "clean-archive", etag, map[string]any{"reason": "Archive empty positive control"})
	requireStatus(t, r, 200)
	me := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader", "", "", nil)
	requireStatus(t, me, 200)
	var caller struct {
		MembershipID string `json:"membershipId"`
	}
	decodeResult(t, me, &caller)
	group, etag := create("active-member-control")
	memberReply := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+group.ID+"/members", "admin", "add-active-member", "", map[string]any{"membershipId": caller.MembershipID, "reason": "Retain an active relation"})
	requireStatus(t, memberReply, 201)
	var member identity.GroupMember
	decodeResult(t, memberReply, &member)
	archived := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+group.ID+":archive", "admin", "archive-with-member", etag, map[string]any{"reason": "Check stated archival prerequisite"})
	t.Logf("empty group archive=200; group with active member archive=%d", archived.status)
	if archived.status != 409 {
		t.Errorf("UI/manual promise that active members must be revoked first is not enforced: got %d", archived.status)
	}

	current := requestJSON(t, server.URL, "GET", "/api/v1/access/groups/"+group.ID, "admin", "", "", nil)
	requireStatus(t, current, 200)
	var kept identity.AccessGroup
	decodeResult(t, current, &kept)
	if kept.State != identity.GroupActive || kept.Revision != group.Revision {
		t.Fatal("failed archive changed group", kept)
	}
	rolesReply := requestJSON(t, server.URL, "GET", "/api/v1/access/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, rolesReply, 200)
	var roles identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, rolesReply, &roles)
	readerID := ""
	for _, role := range roles.Items {
		if role.Builtin && role.Key == "reader" {
			readerID = role.ID
		}
	}
	if readerID == "" {
		t.Fatal("Reader template missing from the valid assignment control")
	}
	assignmentReply := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments", "admin", "assign-before-archive", "", map[string]any{"roleDefinitionId": readerID, "groupId": group.ID, "reason": "Qualify both archival prerequisites"})
	requireStatus(t, assignmentReply, 201)
	var assignment identity.AccessAssignment
	decodeResult(t, assignmentReply, &assignment)
	revoked := requestJSON(t, server.URL, "POST", fmt.Sprintf("/api/v1/access/groups/%s/members/%s:revoke", group.ID, member.ID), "admin", "revoke-before-archive", memberReply.header.Get("ETag"), map[string]any{"reason": "Explicit cleanup before terminal archive"})
	requireStatus(t, revoked, 200)
	stillAssigned := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+group.ID+":archive", "admin", "archive-with-assignment", etag, map[string]any{"reason": "Member cleanup alone is insufficient"})
	requireStatus(t, stillAssigned, 409)
	current = requestJSON(t, server.URL, "GET", "/api/v1/access/groups/"+group.ID, "admin", "", "", nil)
	requireStatus(t, current, 200)
	decodeResult(t, current, &kept)
	if kept.State != identity.GroupActive || kept.Revision != group.Revision {
		t.Fatal("assignment-prerequisite refusal changed the group", kept)
	}
	// A successful revoke with the original assignment ETag also proves that
	// the refused archival did not alter or implicitly revoke that assignment.
	assignmentRevoked := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments/"+assignment.ID+":revoke", "admin", "unassign-before-archive", assignmentReply.header.Get("ETag"), map[string]any{"reason": "Complete explicit assignment cleanup"})
	requireStatus(t, assignmentRevoked, 200)
	final := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+group.ID+":archive", "admin", "archive-after-cleanup", etag, map[string]any{"reason": "Archive after explicit cleanup"})
	requireStatus(t, final, 200)
	replay := requestJSON(t, server.URL, "POST", "/api/v1/access/groups/"+group.ID+":archive", "admin", "archive-after-cleanup", etag, map[string]any{"reason": "Archive after explicit cleanup"})
	requireStatus(t, replay, 200)
	if replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("archive replay not retained")
	}
}
