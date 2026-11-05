"""One bounded current-policy control reused by backup and writer recovery.

All mutations use the owned API. Receipts remain private fixture state; public
reports retain only the successful conclusion. No role SQL or allow cache.
"""

from urllib.parse import urlencode

from core import mutate
from federation import Client
from http_client import decode
from journeys import audit_once, header


def prepare(api, namespace_id, suffix):
    """Retain an active group path and a separately revoked direct path."""
    reader = Client(api.base_url, "local-reader")
    member, _ = reader.request("GET", "/api/v1/me")
    role, _ = mutate(api, "POST", "/api/v1/access/roles", {
        "name": "Recovery author " + suffix,
        "permissions": ["namespace.read", "resource.create", "resource.read"],
        "reason": "Retain exact scoped group authority"}, 201)
    group, _ = mutate(api, "POST", "/api/v1/access/groups", {
        "name": "Recovery group " + suffix, "reason": "Retain direct same-realm membership"}, 201)
    relation, _ = mutate(api, "POST", "/api/v1/access/groups/" + group["id"] + "/members", {
        "membershipId": member["membershipId"], "reason": "Retain group membership"}, 201)
    grant, _ = mutate(api, "POST", "/api/v1/access/assignments", {
        "roleDefinitionId": role["id"], "groupId": group["id"], "namespaceId": namespace_id,
        "reason": "Retain bounded author scope"}, 201)
    editing, _ = mutate(api, "POST", "/api/v1/access/roles", {
        "name": "Revoked recovery editor " + suffix,
        "permissions": ["resource.read", "resource.read_history", "resource.read_content", "resource.edit"], "reason": "Retain revoked authority control"}, 201)
    direct, _ = mutate(api, "POST", "/api/v1/access/assignments", {
        "roleDefinitionId": editing["id"], "membershipId": member["membershipId"],
        "namespaceId": namespace_id, "reason": "Create revocation control"}, 201)
    revoked, _ = mutate(api, "POST", "/api/v1/access/assignments/" + direct["id"] + ":revoke", {
        "reason": "Must remain revoked after recovery"}, etag='"rev-1"')
    body = {"namespaceId": namespace_id, "kind": "skill",
            "canonicalIdentifier": "urn:air:catalog.example.com:recovery:" + suffix}
    key = {"Idempotency-Key": "policy-recovery-" + suffix}
    raw, headers = reader.request("POST", "/api/v1/resources", body, (201,), key, decode_body=False)
    resource = decode(raw)
    assert resource["namespaceId"] == namespace_id and resource["createdBy"] == "local-reader"
    assert header(headers, "ETag") == '"rev-1"'
    receipt = {"namespace": namespace_id, "body": body, "key": key, "raw": raw,
               "resource": resource, "paths": {
                   "/api/v1/access/roles/" + role["id"]: role,
                   "/api/v1/access/roles/" + editing["id"]: editing,
                   "/api/v1/access/groups/" + group["id"]: group},
               "assignmentQueries": {
                   "/api/v1/access/assignments?" + urlencode({"groupId": group["id"], "roleDefinitionId": role["id"], "limit": 2}): grant,
                   "/api/v1/access/assignments?" + urlencode({"membershipId": member["membershipId"], "roleDefinitionId": editing["id"], "limit": 2}): revoked},
               "group": group["id"], "relation": relation}
    verify(api, receipt)
    return receipt


def verify(api, receipt):
    """Read current rows, prove exact scope and revoked denial, then byte replay."""
    reader = Client(api.base_url, "local-reader")
    for path, expected in receipt["paths"].items():
        assert api.request("GET", path)[0] == expected, "recovery changed retained policy"
    # Assignments have bounded filtered List, not an exact GET route. Both
    # known paths must be complete, retain their rows and remain independently scoped.
    for path, expected in receipt["assignmentQueries"].items():
        page, _ = api.request("GET", path)
        assert page["items"] == [expected] and not page.get("nextCursor"), "recovery changed or truncated retained assignments"
    members, _ = api.request("GET", "/api/v1/access/groups/" + receipt["group"] + "/members?limit=2")
    assert members["items"] == [receipt["relation"]] and not members.get("nextCursor")
    decision, _ = reader.request("POST", "/api/v1/access/capabilities", {"checks": [
        {"permission": "resource.create", "namespaceId": receipt["namespace"]},
        {"permission": "resource.create"},
        {"permission": "resource.edit", "namespaceId": receipt["namespace"]}]})
    assert decision.get("catalogAllowed") == [True, False, False], "recovery broadened or lost current policy"
    replay, headers = reader.request("POST", "/api/v1/resources", receipt["body"], (201,),
                                     receipt["key"], decode_body=False)
    assert replay == receipt["raw"] and header(headers, "Idempotency-Replayed") == "true"
    assert header(headers, "ETag") == '"rev-1"'
    audit_once(api, "resource.created", "resource", receipt["resource"]["id"])
