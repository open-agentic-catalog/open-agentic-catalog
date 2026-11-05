#!/usr/bin/env python3
"""Small fixed packaged journeys sharing the existing seeded identities."""

import argparse
import datetime
import urllib.parse

from federation import Client, key


def header(headers, name):
    return next((value for key, value in headers.items() if key.lower() == name.lower()), None)


def namespace_state(value):
    # PostgreSQL stores timestamps at microsecond precision. Compare governed
    # state on readback; exact replay is independently byte/value bound below.
    return {name: value.get(name) for name in ("id", "tenantId", "slug", "displayName", "description", "revision")}


def audit_once(api, action, target_type, target_id):
    now = datetime.datetime.now(datetime.timezone.utc)
    query = urllib.parse.urlencode({"action": action, "targetType": target_type, "targetId": target_id,
                                   "occurredFrom": (now-datetime.timedelta(minutes=20)).isoformat(),
                                   "occurredBefore": (now+datetime.timedelta(minutes=1)).isoformat(), "limit": 2})
    events, _ = api.request("GET", "/api/v1/audit-events?" + query)
    assert len(events["items"]) == 1 and not events.get("nextCursor"), "mutation audit missing or duplicated"
    event = events["items"][0]
    assert (event["action"], event["targetType"], event["targetId"]) == (action, target_type, target_id), "mutation audit does not match requested target"


def namespace_journey(base, run_id):
    api = Client(base, "local-admin")
    other = Client(base, "other-tenant-admin")
    path = "/api/v1/namespaces"
    body = {"slug": run_id + "-contract", "displayName": "Contract journey"}
    create_headers = {"Idempotency-Key": run_id + "-contract-create"}
    created, headers = api.request("POST", path, body, (201,), create_headers)
    assert all(created.get(name) == value for name, value in body.items()), "namespace creation ignored requested values"
    etag = header(headers, "ETag")
    assert etag == '"rev-1"', "namespace create ETag mismatch"
    replay, replay_headers = api.request("POST", path, body, (201,), create_headers)
    assert replay == created and header(replay_headers, "ETag") == etag, "namespace create replay mismatch"
    exact = path + "/" + created["id"]
    read, read_headers = api.request("GET", exact)
    assert namespace_state(read) == namespace_state(created) and header(read_headers, "ETag") == etag, "namespace readback mismatch"
    other.request("GET", exact, expected=(404,))
    patch_body = {"displayName": "Contract updated", "reason": "Owned response qualification"}
    patch_headers = {"Idempotency-Key": run_id + "-contract-patch", "If-Match": etag}
    changed, changed_headers = api.request("PATCH", exact, patch_body, headers=patch_headers)
    assert namespace_state(changed) == {**namespace_state(created), "displayName": patch_body["displayName"], "revision": 2}, "namespace update ignored input or changed unrelated state"
    assert changed["revision"] == 2 and header(changed_headers, "ETag") == '"rev-2"', "namespace patch ETag mismatch"
    replay, replay_headers = api.request("PATCH", exact, patch_body, headers=patch_headers)
    assert replay == changed and header(replay_headers, "ETag") == '"rev-2"', "namespace patch replay mismatch"
    # Bind a retained command to both its original input and precondition.
    # Each conflicting request is otherwise valid and uses the original key.
    for conflicting_body, conflicting_headers in (
        ({**patch_body, "displayName": "Must not replace retained command"}, patch_headers),
        (patch_body, {**patch_headers, "If-Match": header(changed_headers, "ETag")}),
    ):
        problem, response_headers = api.request("PATCH", exact, conflicting_body, (409,), conflicting_headers)
        assert problem["code"] == "IDEMPOTENCY_KEY_REUSED" and not header(response_headers, "Idempotency-Replayed"), "changed command reused cached success"
    replay, replay_headers = api.request("PATCH", exact, patch_body, headers=patch_headers)
    assert replay == changed and header(replay_headers, "ETag") == '"rev-2"' and header(replay_headers, "Idempotency-Replayed") == "true", "conflicting requests damaged original replay"
    api.request("PATCH", exact, patch_body, (412,), {"Idempotency-Key": key("stale"), "If-Match": etag})
    read, _ = api.request("GET", exact)
    assert namespace_state(read) == namespace_state(changed), "stale mutation changed namespace"
    # Bind to the exact target, not a tenant-wide count that worker and read
    # audit can legitimately change. A bounded interval is explicit as well.
    for action in ("namespace.created", "namespace.updated"):
        audit_once(api, action, "namespace", created["id"])
    print("Namespace method/readback/rejection/replay journey passed.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--run-id", required=True)
    args = parser.parse_args()
    namespace_journey(args.base_url, args.run_id)


if __name__ == "__main__":
    main()
