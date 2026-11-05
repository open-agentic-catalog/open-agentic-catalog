//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"net/http"
	"testing"
)

func TestAuthorizationAdministrationHTTPContractsAndReplay(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	const base = "/api/v1/access"
	descriptors := requestJSON(t, server.URL, http.MethodGet, base+"/permissions", "reader", "", "", nil)
	requireStatus(t, descriptors, 200)
	var permissions []identity.PermissionDescriptor
	decodeResult(t, descriptors, &permissions)
	if len(permissions) != 50 {
		t.Fatal("finite catalog")
	}
	var maximum []identity.Permission
	for _, descriptor := range permissions {
		if descriptor.Delegable {
			maximum = append(maximum, descriptor.Key)
		}
	}
	maximumInput := map[string]any{"name": "All delegable permissions", "permissions": maximum, "reason": "Verify finite custom role capacity"}
	rawInput, err := json.Marshal(maximumInput)
	if err != nil {
		t.Fatal(err)
	}
	requireControlSchema(t, "RoleDefinitionInput", rawInput)
	maximumRole := requestJSON(t, server.URL, http.MethodPost, base+"/roles", "admin", "maximum-custom-role", "", maximumInput)
	requireStatus(t, maximumRole, http.StatusCreated)
	requireControlSchema(t, "RoleDefinition", maximumRole.body)
	var retained identity.RoleDefinition
	decodeResult(t, maximumRole, &retained)
	if len(retained.Permissions) != len(maximum) || len(maximum) != 49 {
		t.Fatal("maximum role lost permissions", retained)
	}
	// At the valid cardinality, protected authority still cannot be delegated.
	maximumInput["permissions"] = append(append([]identity.Permission{}, maximum[1:]...), "access.manage")
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, base+"/roles", "admin", "protected-custom-role", "", maximumInput), http.StatusBadRequest, catalog.CodeInvalidArgument)
	maximumInput["permissions"] = append(append([]identity.Permission{}, maximum...), "access.manage")
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, base+"/roles", "admin", "oversize-custom-role", "", maximumInput), http.StatusBadRequest, catalog.CodeInvalidArgument)
	capabilities := requestJSON(t, server.URL, http.MethodPost, base+"/capabilities", "reader", "", "", map[string]any{"checks": []any{map[string]any{"permission": "resource.read"}, map[string]any{"permission": "access.manage"}}})
	requireStatus(t, capabilities, 200)
	requireControlSchema(t, "PermissionDecision", capabilities.body)
	var decision identity.PermissionDecision
	decodeResult(t, capabilities, &decision)
	if len(decision.Allowed) != 2 || !decision.Allowed[0] || decision.Allowed[1] {
		t.Fatal("capability decisions", decision)
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, base+"/roles", "reader", "", "", nil), 403, catalog.CodeForbidden)
	input := map[string]any{"name": "HTTP custom role", "permissions": []string{"resource.read"}, "reason": "Create reviewed role"}
	created := requestJSON(t, server.URL, http.MethodPost, base+"/roles", "admin", "create-access-role", "", input)
	requireStatus(t, created, 201)
	requireControlSchema(t, "RoleDefinition", created.body)
	var role identity.RoleDefinition
	decodeResult(t, created, &role)
	if role.Builtin || role.Key != "" || created.header.Get("ETag") != `"rev-1"` || created.header.Get("Location") != base+"/roles/"+role.ID {
		t.Fatal("create projection", role, created.header)
	}
	replay := requestJSON(t, server.URL, http.MethodPost, base+"/roles", "admin", "create-access-role", "", input)
	requireStatus(t, replay, 201)
	if replay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(created.body, replay.body) {
		t.Fatal("create replay changed result")
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, base+"/roles/"+role.ID, "other-admin", "", "", nil), 404, catalog.CodeNotFound)
	roleInput := map[string]any{"name": "Updated role", "permissions": []string{"namespace.read"}, "reason": "Replace exact permissions"}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPatch, base+"/roles/"+role.ID, "admin", "missing-precondition", "", roleInput), 412, catalog.CodePreconditionFailed)
	updated := requestJSON(t, server.URL, http.MethodPatch, base+"/roles/"+role.ID, "admin", "update-access-role", created.header.Get("ETag"), roleInput)
	requireStatus(t, updated, 200)
	requireControlSchema(t, "RoleDefinition", updated.body)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPatch, base+"/roles/"+role.ID, "admin", "stale-precondition", created.header.Get("ETag"), roleInput), 412, catalog.CodePreconditionFailed)
	groupResult := requestJSON(t, server.URL, http.MethodPost, base+"/groups", "admin", "create-access-group", "", map[string]any{"name": "HTTP group", "reason": "Create group"})
	requireStatus(t, groupResult, 201)
	requireControlSchema(t, "AccessGroup", groupResult.body)
	var group identity.AccessGroup
	decodeResult(t, groupResult, &group)
	self := requestJSON(t, server.URL, http.MethodGet, "/api/v1/me", "reader", "", "", nil)
	requireStatus(t, self, 200)
	var me struct {
		MembershipID string `json:"membershipId"`
	}
	decodeResult(t, self, &me)
	if me.MembershipID == "" {
		t.Fatal("self member missing", string(self.body))
	}
	memberResult := requestJSON(t, server.URL, http.MethodPost, base+"/groups/"+group.ID+"/members", "admin", "add-group-member", "", map[string]any{"membershipId": me.MembershipID, "reason": "Add direct member"})
	requireStatus(t, memberResult, 201)
	requireControlSchema(t, "AccessGroupMember", memberResult.body)
	var member identity.GroupMember
	decodeResult(t, memberResult, &member)
	assignmentResult := requestJSON(t, server.URL, http.MethodPost, base+"/assignments", "admin", "grant-group-role", "", map[string]any{"roleDefinitionId": role.ID, "groupId": group.ID, "reason": "Grant group role"})
	requireStatus(t, assignmentResult, 201)
	requireControlSchema(t, "AccessAssignment", assignmentResult.body)
	var assignment identity.AccessAssignment
	decodeResult(t, assignmentResult, &assignment)
	for path, schema := range map[string]string{base + "/roles?limit=1": "RoleDefinitionPage", base + "/groups?limit=1": "AccessGroupPage", base + "/roles/" + role.ID + "/assignments?limit=1": "AccessAssignmentPage", base + "/groups/" + group.ID + "/members?limit=1": "AccessGroupMemberPage", base + "/assignments?groupId=" + group.ID: "AccessAssignmentPage", base + "/self?limit=1": "EffectiveAccessPage"} {
		v := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
		requireStatus(t, v, 200)
		requireControlSchema(t, schema, v.body)
	}
	own := requestJSON(t, server.URL, http.MethodGet, base+"/self?limit=100", "reader", "", "", nil)
	requireStatus(t, own, 200)
	requireControlSchema(t, "EffectiveAccessPage", own.body)
	var ownPaths identity.AuthorizationPage[identity.EffectiveAssignment]
	decodeResult(t, own, &ownPaths)
	foundGroup := false
	for _, item := range ownPaths.Items {
		if item.Assignment.ID == assignment.ID {
			foundGroup = item.GroupName == "HTTP group" && item.Assignment.GroupID == group.ID
		} else if item.Assignment.GroupID == "" && item.GroupName != "" {
			t.Fatal("direct self path inherited another group's caption")
		}
	}
	if !foundGroup {
		t.Fatal("ordinary recipient lacked its exact group caption")
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, base+"/groups/"+group.ID, "reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	removed := requestJSON(t, server.URL, http.MethodPost, base+"/groups/"+group.ID+"/members/"+member.ID+":revoke", "admin", "remove-group-member", memberResult.header.Get("ETag"), map[string]any{"reason": "Remove member path"})
	requireStatus(t, removed, 200)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, base+"/groups/"+group.ID+":archive", "admin", "archive-assigned-group", groupResult.header.Get("ETag"), map[string]any{"reason": "Archive group"}), 409, catalog.CodeConflict)
	revoked := requestJSON(t, server.URL, http.MethodPost, base+"/assignments/"+assignment.ID+":revoke", "admin", "revoke-group-role", assignmentResult.header.Get("ETag"), map[string]any{"reason": "Revoke grant"})
	requireStatus(t, revoked, 200)
	archived := requestJSON(t, server.URL, http.MethodPost, base+"/groups/"+group.ID+":archive", "admin", "archive-unused-group", groupResult.header.Get("ETag"), map[string]any{"reason": "Archive unused group"})
	requireStatus(t, archived, 200)
	archivedRole := requestJSON(t, server.URL, http.MethodPost, base+"/roles/"+role.ID+":archive", "admin", "archive-unused-role", updated.header.Get("ETag"), map[string]any{"reason": "Archive unused definition"})
	requireStatus(t, archivedRole, 200)
}
func TestAuthorizationAdministrationRejectsUntrustedBodies(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	for i, body := range []string{`{"name":"Bad","permissions":["resource.read"],"reason":"Test","builtin":true}`, `{"name":"Bad","permissions":["access.manage"],"reason":"Test"}`, `{"name":"Bad","permissions":["resource.edit"],"reason":"Missing companions"}`, `{"name":"Bad","name":"Duplicate","permissions":["resource.read"],"reason":"Test"}`, `{"name":null,"permissions":["resource.read"],"reason":"Test"}`, `{"name":"Bad","permissions":["resource.read"],"reason":"Test"} {}`, `{"Name":"Wrong case","permissions":["resource.read"],"reason":"Test"}`} {
		result := requestWithHeaders(t, server.URL, http.MethodPost, "/api/v1/access/roles", []byte(body), map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json", "Idempotency-Key": "invalid-access-" + string(rune('a'+i))})
		requireControlProblem(t, result, 400, catalog.CodeInvalidArgument)
	}
	result := requestJSON(t, server.URL, http.MethodGet, "/api/v1/access/roles?unexpected=1", "admin", "", "", nil)
	requireControlProblem(t, result, 400, catalog.CodeInvalidArgument)
	result = requestJSON(t, server.URL, http.MethodPost, "/api/v1/access/capabilities", "reader", "", "", map[string]any{"checks": []any{map[string]any{"permission": "access.manage", "namespaceId": "00000000-0000-7000-8000-000000000010"}}})
	requireControlProblem(t, result, 400, catalog.CodeInvalidArgument)
	page := requestJSON(t, server.URL, http.MethodGet, "/api/v1/access/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, page, 200)
	var roles identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, page, &roles)
	if len(roles.Items) != 14 {
		t.Fatal("malformed requests changed role data")
	}
}

func TestAuthorizationAdministrationReplayRequiresCurrentGrant(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	const base = "/api/v1/access"
	memberResult := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, memberResult, 200)
	var member struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, memberResult, &member)
	rolesResult := requestJSON(t, server.URL, "GET", base+"/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, rolesResult, 200)
	var roles identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, rolesResult, &roles)
	adminRole := ""
	for _, r := range roles.Items {
		if r.Key == "tenant_admin" {
			adminRole = r.ID
		}
	}
	if adminRole == "" {
		t.Fatal("protected template absent")
	}
	granted := requestJSON(t, server.URL, "POST", base+"/assignments", "admin", "temporary-administrator", "", map[string]any{"roleDefinitionId": adminRole, "membershipId": member.ID, "reason": "Create temporary direct administrator"})
	requireStatus(t, granted, 201)
	var assignment identity.AccessAssignment
	decodeResult(t, granted, &assignment)
	input := map[string]any{"name": "Retained private role", "permissions": []string{"resource.read"}, "reason": "Create role through current direct grant"}
	created := requestJSON(t, server.URL, "POST", base+"/roles", "reader-two", "replayed-private-definition", "", input)
	requireStatus(t, created, 201)
	// The legacy reader projection and command identity do not change when this
	// new direct administrator grant is revoked. Replay must still recheck it.
	revoked := requestJSON(t, server.URL, "POST", base+"/assignments/"+assignment.ID+":revoke", "admin", "remove-temporary-administrator", granted.header.Get("ETag"), map[string]any{"reason": "Revoke temporary administration"})
	requireStatus(t, revoked, 200)
	refused := requestJSON(t, server.URL, "POST", base+"/roles", "reader-two", "replayed-private-definition", "", input)
	requireControlProblem(t, refused, 403, catalog.CodeForbidden)
	if refused.header.Get("Idempotency-Replayed") == "true" || bytes.Contains(refused.body, []byte("Retained private role")) {
		t.Fatal("revoked administrator received retained private response")
	}
	// Restoring authority uses a new immutable assignment, not revival of the
	// revoked path. The stable command must resume disclosure, not execute again.
	regranted := requestJSON(t, server.URL, "POST", base+"/assignments", "admin", "restore-temporary-administrator", "", map[string]any{"roleDefinitionId": adminRole, "membershipId": member.ID, "reason": "Restore direct administrator authority"})
	requireStatus(t, regranted, 201)
	var replacement identity.AccessAssignment
	decodeResult(t, regranted, &replacement)
	if replacement.ID == assignment.ID || replacement.State != identity.AssignmentActive {
		t.Fatal("restored grant rewrote terminal assignment history")
	}
	replayed := requestJSON(t, server.URL, "POST", base+"/roles", "reader-two", "replayed-private-definition", "", input)
	requireStatus(t, replayed, 201)
	if replayed.header.Get("Idempotency-Replayed") != "true" || replayed.header.Get("ETag") != created.header.Get("ETag") || !bytes.Equal(replayed.body, created.body) {
		t.Fatal("restored authority failed to disclose the exact original response")
	}
	directory := requestJSON(t, server.URL, "GET", base+"/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, directory, 200)
	decodeResult(t, directory, &roles)
	matches := 0
	for _, role := range roles.Items {
		if role.Name == "Retained private role" {
			matches++
		}
	}
	if matches != 1 || roles.NextCursor != "" {
		t.Fatal("restored replay repeated the mutation or hid a duplicate")
	}
}

func TestGroupPolicyChangesImmediatelyGovernHTTPHintsAndRetainedCommands(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	const base = "/api/v1/access"
	nsReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "group-handoff-ns", "", map[string]any{"slug": "group-handoff", "displayName": "Group handoff"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	meReply := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, meReply, 200)
	var me struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, meReply, &me)
	roleReply := requestJSON(t, server.URL, "POST", base+"/roles", "admin", "group-handoff-role", "", map[string]any{"name": "Grouped author", "permissions": []string{"namespace.read", "resource.create"}, "reason": "Authorize grouped author"})
	requireStatus(t, roleReply, 201)
	var role identity.RoleDefinition
	decodeResult(t, roleReply, &role)
	groupReply := requestJSON(t, server.URL, "POST", base+"/groups", "admin", "group-handoff-group", "", map[string]any{"name": "Authors", "reason": "Create explicit group"})
	requireStatus(t, groupReply, 201)
	var group identity.AccessGroup
	decodeResult(t, groupReply, &group)
	memberInput := map[string]any{"membershipId": me.ID, "reason": "Add explicit grouped author"}
	memberReply := requestJSON(t, server.URL, "POST", base+"/groups/"+group.ID+"/members", "admin", "group-handoff-member", "", memberInput)
	requireStatus(t, memberReply, 201)
	var member identity.GroupMember
	decodeResult(t, memberReply, &member)
	hint := func(want bool) int64 {
		t.Helper()
		result := requestJSON(t, server.URL, "POST", base+"/capabilities", "reader-two", "", "", map[string]any{"checks": []identity.PermissionCheck{{Permission: "resource.create", NamespaceID: ns.ID}}})
		requireStatus(t, result, 200)
		requireControlSchema(t, "PermissionDecision", result.body)
		var decision identity.PermissionDecision
		decodeResult(t, result, &decision)
		if len(decision.Allowed) != 1 || decision.Allowed[0] != want || decision.Revision < 1 {
			t.Fatal("HTTP permission hint ignored current group authority", decision)
		}
		return decision.Revision
	}
	revision := hint(false)
	grant := requestJSON(t, server.URL, "POST", base+"/assignments", "admin", "group-handoff-grant", "", map[string]any{"roleDefinitionId": role.ID, "groupId": group.ID, "namespaceId": ns.ID, "reason": "Authorize this namespace"})
	requireStatus(t, grant, 201)
	if next := hint(true); next <= revision {
		t.Fatal("group grant did not advance current policy")
	}
	input := map[string]any{"namespaceId": ns.ID, "kind": "agent", "canonicalIdentifier": "urn:air:catalog.example.com:group-handoff:stable"}
	created := requestJSON(t, server.URL, "POST", "/api/v1/resources", "reader-two", "group-stable-command", "", input)
	requireStatus(t, created, 201)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	assertReplay := func(allowed bool) {
		t.Helper()
		result := requestJSON(t, server.URL, "POST", "/api/v1/resources", "reader-two", "group-stable-command", "", input)
		if allowed {
			requireStatus(t, result, 201)
			if result.header.Get("Idempotency-Replayed") != "true" || result.header.Get("ETag") != created.header.Get("ETag") || !bytes.Equal(result.body, created.body) {
				t.Fatal("group recovery reexecuted or altered retained command")
			}
			return
		}
		requireControlProblem(t, result, 403, catalog.CodeForbidden)
		if result.header.Get("Idempotency-Replayed") != "" || result.header.Get("ETag") != "" || bytes.Contains(result.body, []byte(resource.ID)) {
			t.Fatal("removed group authority disclosed retained resource")
		}
	}
	assertReplay(true)
	suspended := requestJSON(t, server.URL, "POST", base+"/groups/"+group.ID+":suspend", "admin", "group-handoff-suspend", groupReply.header.Get("ETag"), map[string]any{"reason": "Suspend grouped authority"})
	requireStatus(t, suspended, 200)
	hint(false)
	assertReplay(false)
	reactivated := requestJSON(t, server.URL, "POST", base+"/groups/"+group.ID+":reactivate", "admin", "group-handoff-reactivate", suspended.header.Get("ETag"), map[string]any{"reason": "Restore grouped authority"})
	requireStatus(t, reactivated, 200)
	hint(true)
	assertReplay(true)
	revoked := requestJSON(t, server.URL, "POST", base+"/groups/"+group.ID+"/members/"+member.ID+":revoke", "admin", "group-handoff-revoke-member", memberReply.header.Get("ETag"), map[string]any{"reason": "Remove direct group relation"})
	requireStatus(t, revoked, 200)
	hint(false)
	assertReplay(false)
	added := requestJSON(t, server.URL, "POST", base+"/groups/"+group.ID+"/members", "admin", "group-handoff-restore-member", "", memberInput)
	requireStatus(t, added, 201)
	hint(true)
	assertReplay(true)
	history := requestJSON(t, server.URL, "GET", base+"/groups/"+group.ID+"/members?limit=100", "admin", "", "", nil)
	requireStatus(t, history, 200)
	var page identity.AuthorizationPage[identity.GroupMember]
	decodeResult(t, history, &page)
	active, terminal := 0, 0
	for _, item := range page.Items {
		if item.MembershipID != me.ID {
			t.Fatal("unexpected group history member")
		}
		if item.ID == member.ID && item.State == identity.AssignmentRevoked && item.Revision == member.Revision+1 && item.RevokedAt != nil {
			terminal++
		}
		if item.ID != member.ID && item.State == identity.AssignmentActive {
			active++
		}
	}
	if len(page.Items) != 2 || page.NextCursor != "" || active != 1 || terminal != 1 {
		t.Fatal("group recovery rewrote history or duplicated membership")
	}
}

// Real HTTP commands retain one stable membership key while current policy
// governs disclosure; role edits never create another execution lane.
func TestDomainReplayAfterPolicyChangeDeletionAndRevocation(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	nsReply := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "replay-ns", "", map[string]any{"slug": "replay", "displayName": "Replay"})
	requireStatus(t, nsReply, 201)
	var ns catalog.Namespace
	decodeResult(t, nsReply, &ns)
	memberReply := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, memberReply, 200)
	var member struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, memberReply, &member)
	roleReply := requestJSON(t, server.URL, "POST", "/api/v1/access/roles", "admin", "replay-author-role", "", map[string]any{"name": "Scoped author", "permissions": []string{"resource.create", "namespace.read"}, "reason": "Authorize scoped author"})
	requireStatus(t, roleReply, 201)
	var role identity.RoleDefinition
	decodeResult(t, roleReply, &role)
	grantReply := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments", "admin", "replay-author-grant", "", map[string]any{"membershipId": member.ID, "roleDefinitionId": role.ID, "namespaceId": ns.ID, "reason": "Authorize one namespace"})
	requireStatus(t, grantReply, 201)
	var grant identity.AccessAssignment
	decodeResult(t, grantReply, &grant)
	input := map[string]any{"namespaceId": ns.ID, "kind": "agent", "canonicalIdentifier": "urn:air:catalog.example.com:replay:stable"}
	created := requestJSON(t, server.URL, "POST", "/api/v1/resources", "reader-two", "stable-domain-command", "", input)
	requireStatus(t, created, 201)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	// An unrelated definition advances the tenant revision without revoking this command.
	requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/access/roles", "admin", "unrelated-replay-role", "", map[string]any{"name": "Unrelated", "permissions": []string{"namespace.read"}, "reason": "Unrelated policy change"}), 201)
	assertReplay := func() {
		t.Helper()
		result := requestJSON(t, server.URL, "POST", "/api/v1/resources", "reader-two", "stable-domain-command", "", input)
		requireStatus(t, result, 201)
		if result.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(created.body, result.body) {
			t.Fatal("command executed again or retained response changed")
		}
	}
	assertReplay()
	requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/resources/"+resource.ID+":delete", "admin", "delete-replay-target", created.header.Get("ETag"), map[string]any{"reason": "Discard retained test draft"}), 204)
	assertReplay() // No old lifecycle/If-Match re-execution after deletion.
	requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/access/assignments/"+grant.ID+":revoke", "admin", "revoke-replay-author", grantReply.header.Get("ETag"), map[string]any{"reason": "Withdraw scoped write authority"}), 200)
	refused := requestJSON(t, server.URL, "POST", "/api/v1/resources", "reader-two", "stable-domain-command", "", input)
	requireControlProblem(t, refused, 403, catalog.CodeForbidden)
	if refused.header.Get("Idempotency-Replayed") != "" || bytes.Contains(refused.body, []byte(resource.ID)) {
		t.Fatal("revoked caller received retained object")
	}
}

func TestWebhookSecretReplayRequiresCurrentOriginalPermission(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	rolesReply := requestJSON(t, server.URL, "GET", "/api/v1/access/roles?limit=100", "admin", "", "", nil)
	requireStatus(t, rolesReply, 200)
	var roles identity.AuthorizationPage[identity.RoleDefinition]
	decodeResult(t, rolesReply, &roles)
	roleID := ""
	for _, role := range roles.Items {
		if role.Key == "webhook_admin" {
			roleID = role.ID
		}
	}
	if roleID == "" {
		t.Fatal("webhook template absent")
	}
	meReply := requestJSON(t, server.URL, "GET", "/api/v1/me", "reader-two", "", "", nil)
	requireStatus(t, meReply, 200)
	var me struct {
		ID string `json:"membershipId"`
	}
	decodeResult(t, meReply, &me)
	grantReply := requestJSON(t, server.URL, "POST", "/api/v1/access/assignments", "admin", "temporary-webhook-authority", "", map[string]any{"membershipId": me.ID, "roleDefinitionId": roleID, "reason": "Temporary webhook qualification"})
	requireStatus(t, grantReply, 201)
	var grant identity.AccessAssignment
	decodeResult(t, grantReply, &grant)
	input := map[string]any{"name": "Private receiver", "url": "https://hooks.example.com/replay", "eventTypes": []string{"*"}}
	created := requestJSON(t, server.URL, "POST", "/api/v1/webhooks", "reader-two", "retained-secret", "", input)
	requireStatus(t, created, 201)
	replay := requestJSON(t, server.URL, "POST", "/api/v1/webhooks", "reader-two", "retained-secret", "", input)
	requireStatus(t, replay, 201)
	if !bytes.Equal(created.body, replay.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("one-time secret changed or command repeated")
	}
	requireStatus(t, requestJSON(t, server.URL, "POST", "/api/v1/access/assignments/"+grant.ID+":revoke", "admin", "revoke-webhook-authority", grantReply.header.Get("ETag"), map[string]any{"reason": "Withdraw webhook authority"}), 200)
	refused := requestJSON(t, server.URL, "POST", "/api/v1/webhooks", "reader-two", "retained-secret", "", input)
	requireControlProblem(t, refused, 403, catalog.CodeForbidden)
	if bytes.Contains(refused.body, []byte("signingSecret")) || refused.header.Get("Idempotency-Replayed") != "" {
		t.Fatal("retained secret disclosed after revocation")
	}
}

func TestEarlyMalformedCommandReplaysSafeValidationProblem(t *testing.T) {
	server := newWebhookHTTPServer(t)
	input := map[string]any{"unknown": "not a namespace"}
	first := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "early-validation-replay", "", input)
	requireControlProblem(t, first, 400, catalog.CodeInvalidArgument)
	replay := requestJSON(t, server.URL, "POST", "/api/v1/namespaces", "admin", "early-validation-replay", "", input)
	requireStatus(t, replay, 400)
	requireControlSchema(t, "Problem", replay.body)
	if replay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.body, first.body) {
		t.Fatal("safe parser error replay changed")
	}
}
