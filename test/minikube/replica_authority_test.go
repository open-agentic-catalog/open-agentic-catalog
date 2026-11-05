//go:build minikube

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Script each peer independently: the oracle cannot pass by skipping a replica,
// changing the retained key, or manufacturing restored membership/response state.
func TestReplicaAuthorityLifecycle(t *testing.T) {
	for _, fault := range []string{"", "membership", "grant-membership", "grant-etag", "restore-membership", "create-state", "replay-bytes", "replay-etag", "read-state", "audit-duplicate", "revoke-state", "revoked-success", "revoked-replay", "revoked-code", "cleanup"} {
		t.Run(fault, func(t *testing.T) {
			type step struct {
				actor                    string
				index                    int
				method, path, key, match string
				body                     any
				response                 httpResult
			}
			var steps []step
			result := func(status int, etag string, value any) httpResult {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return httpResult{Status: status, Header: http.Header{"Etag": {etag}}, Body: raw}
			}
			problem := func(status int, code string) httpResult {
				return result(status, "", map[string]any{"status": status, "code": code})
			}
			add := func(actor string, index int, method, path, key, match string, body any, response httpResult) {
				steps = append(steps, step{actor, index, method, path, key, match, body, response})
			}
			membership := "member"
			if fault == "membership" {
				membership = ""
			}
			add("reader", 0, "GET", "/api/v1/me", "", "", nil, result(200, "", map[string]string{"membershipId": membership, "subject": "oidc-reader"}))
			body := map[string]string{"namespaceId": "ns", "kind": "skill", "canonicalIdentifier": "urn:air:catalog.example.com:replicas:fixture"}
			add("reader", 1, "POST", "/api/v1/resources", "fixture-denied", "", body, problem(403, "FORBIDDEN"))
			grantBody := map[string]string{"subject": "oidc-reader", "actorType": "human", "role": "contributor", "namespaceId": "ns", "reason": "Owned replica authority qualification"}
			resource := map[string]any{"id": "resource", "tenantId": primaryTenantID, "namespaceId": "ns", "kind": "skill", "canonicalIdentifier": body["canonicalIdentifier"], "createdBy": "oidc-reader", "revision": 1}
			created := result(201, `"rev-1"`, resource)
			for stage := range 2 {
				id := fmt.Sprintf("grant-%d", stage)
				grant := map[string]any{"id": id, "tenantId": primaryTenantID, "membershipId": "member", "subject": "oidc-reader", "role": "contributor", "namespaceId": "ns", "state": "active"}
				if fault == "grant-membership" || fault == "restore-membership" && stage == 1 {
					grant["membershipId"] = "different"
				}
				grantResult := result(201, `"rev-1"`, grant)
				if fault == "grant-etag" {
					grantResult.Header.Del("ETag")
				}
				add("admin", stage, "POST", "/api/v1/role-assignments", "fixture"+[]string{"-grant", "-restore"}[stage], "", grantBody, grantResult)
				if stage == 0 {
					original := created
					if fault == "create-state" {
						original.Body = []byte(`{"id":"wrong","revision":9}`)
					}
					add("reader", 0, "POST", "/api/v1/resources", "fixture-create", "", body, original)
				}
				for offset := 1; offset <= 3; offset++ {
					replay := created
					replay.Header = created.Header.Clone()
					replay.Header.Set("Idempotency-Replayed", "true")
					if stage == 1 && offset == 2 && fault == "replay-bytes" {
						replay.Body = append([]byte(" "), replay.Body...)
					}
					if stage == 1 && offset == 2 && fault == "replay-etag" {
						replay.Header.Del("ETag")
					}
					add("reader", offset%3, "POST", "/api/v1/resources", "fixture-create", "", body, replay)
					read := result(200, `"rev-1"`, resource)
					if stage == 1 && offset == 2 && fault == "read-state" {
						read.Body = []byte(`{"id":"resource","revision":9}`)
					}
					add("admin", offset%3, "GET", "/api/v1/resources/resource", "", "", nil, read)
				}
				audit := result(200, "", map[string]any{"items": []map[string]string{{"targetId": "resource", "action": "resource.created"}}})
				if fault == "audit-duplicate" {
					audit.Body = []byte(`{"items":[{},{}]}`)
				}
				add("admin", 0, "GET", "audit", "", "", nil, audit)
				revoke := result(200, `"rev-2"`, map[string]string{"id": id, "membershipId": "member", "state": "revoked"})
				if fault == "revoke-state" {
					revoke.Body = []byte(`{"state":"active"}`)
				}
				add("admin", (stage+2)%3, "POST", "/api/v1/role-assignments/"+id+":revoke", "fixture"+[]string{"-revoke", "-finish"}[stage], `"rev-1"`, map[string]string{"reason": "Finish owned scoped authority"}, revoke)
				for index := range 3 {
					denied := problem(403, "FORBIDDEN")
					if index == 2 {
						switch fault {
						case "revoked-success":
							denied = created
						case "revoked-replay":
							denied.Header.Set("Idempotency-Replayed", "true")
						case "revoked-code":
							denied = problem(403, "INTERNAL")
						}
					}
					add("reader", index, "POST", "/api/v1/resources", "fixture-create", "", body, denied)
				}
				add("admin", 0, "GET", "audit", "", "", nil, audit)
			}
			deleted := httpResult{Status: 204}
			if fault == "cleanup" {
				deleted.Status = 500
			}
			add("admin", 2, "POST", "/api/v1/resources/resource:delete", "fixture-delete", `"rev-1"`, map[string]string{"reason": "Remove replica authority fixture"}, deleted)
			for index := range 3 {
				add("admin", index, "GET", "/api/v1/resources/resource", "", "", nil, problem(404, "NOT_FOUND"))
			}
			position := 0
			request := func(actor string) replicaRequest {
				return func(index int, method, path, key, match string, body any) (httpResult, error) {
					if position >= len(steps) {
						t.Fatal("unexpected request")
					}
					want := steps[position]
					position++
					if want.path == "audit" {
						if !strings.HasPrefix(path, "/api/v1/audit-events?") || !strings.Contains(path, "action=resource.created") || !strings.Contains(path, "targetId=resource") || !strings.Contains(path, "targetType=resource") || !strings.Contains(path, "limit=2") {
							t.Fatal("audit not bound to originating mutation")
						}
						path = "audit"
					}
					if actor != want.actor || index != want.index || method != want.method || path != want.path || key != want.key || match != want.match || !reflect.DeepEqual(body, want.body) {
						t.Fatalf("step %d: unexpected actor/index/request", position)
					}
					return want.response, nil
				}
			}
			err := (&harness{}).verifyReplicaAuthority(request("admin"), request("reader"), "ns", "fixture")
			if (err == nil) != (fault == "") {
				t.Fatalf("unexpected error: %v", err)
			}
			if fault == "" && position != len(steps) {
				t.Fatalf("skipped %d requests", len(steps)-position)
			}
		})
	}
}
