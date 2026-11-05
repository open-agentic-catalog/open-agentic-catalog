"""Nonempty rating inputs and private pre-backup expectations for the owned drill."""

import datetime
import hashlib
import json

import audit
from core import mutate
from federation import Client
from journeys import header
from runner import Failure, read_document, write_document
from webhooks import TENANT


# These four maintained development identities rate one exact retained version.
OWNERS = {"consented": "local-reader", "private": "local-contributor",
          "withdrawn": "local-reviewer", "moderated": "local-publisher"}
HISTORY = {"consented": [(4, False, "active"), (5, True, "active")],
           "private": [(2, False, "active")],
           "withdrawn": [(1, False, "active"), (0, False, "withdrawn")],
           "moderated": [(3, True, "active"), (0, False, "removed")]}
TOTALS = {"vote_count": 2, "score_sum": 7, "shared_count": 1, "shared_sum": 5}
TABLES = {"rating_targets": ("target_key", 1), "item_ratings": ("id", 4),
          "item_rating_revisions": ("rating_id,revision", 7), "rating_sharing": ("id", 1)}


def require(value):
    """Do not put private opinions or principals in failures or public findings."""
    if not value:
        raise Failure("backup-rating-baseline-mismatch")


def prepare(journey):
    """Create all state through the API, including an edit and explicit sharing."""
    record = next(item for item in journey.retained if item["resource"]["kind"] == "agent")
    target = {"origin": "local", "resourceId": record["resource"]["id"],
              "versionRevisionId": record["versionRevision"]["id"],
              "identifier": record["resource"]["canonicalIdentifier"], "kind": "agent",
              "versionPresent": True, "version": record["version"]["version"]}
    admission = {"target": target, "releaseId": record["release"]["id"]}
    before = audit.request(journey.base, "GET", "/.well-known/ard.json", "local-admin")
    require(before[0] == 200 and bool(header(before[1], "ETag")))
    opinions = {}
    for label, token in OWNERS.items():
        owner = Client(journey.base, token)
        score, consent, _ = HISTORY[label][0]
        receipt, headers = mutate(owner, "POST", "/api/v1/ratings",
                                  {**admission, "score": score, "consent": consent}, 201)
        require(receipt["revision"] == 1 and header(headers, "ETag") == '"rev-1"')
        identifier = receipt["id"]
        if label == "consented":
            receipt, headers = mutate(owner, "POST", "/api/v1/ratings",
                {**admission, "score": 5, "consent": True}, etag=header(headers, "ETag"))
        elif label in ("withdrawn", "moderated"):
            action = "withdraw" if label == "withdrawn" else "remove"
            actor = owner if label == "withdrawn" else journey.api
            body = {} if label == "withdrawn" else {"reason": "Owned backup moderation"}
            receipt, headers = mutate(actor, "POST", "/api/v1/ratings/" + identifier + ":" + action,
                                      body, etag=header(headers, "ETag"))
        revision = len(HISTORY[label])
        require(receipt["id"] == identifier and receipt["revision"] == revision
                and header(headers, "ETag") == '"rev-' + str(revision) + '"')
        opinions[label] = {"id": identifier, "revision": revision, "etag": header(headers, "ETag")}
    sharing_path = "/api/v1/ratings/sharing?host=127.0.0.1&audience=anonymous"
    current, _ = journey.api.request("GET", sharing_path)
    require(current["revision"] == 0 and current["enabled"] is False)
    choice, headers = mutate(journey.api, "POST", "/api/v1/ratings/sharing", {
        "host": "127.0.0.1", "audience": "anonymous", "enabled": True,
        "reason": "Owned backup sharing choice"})
    require(choice["revision"] == 1 and header(headers, "ETag") == '"rev-1"')
    after = audit.request(journey.base, "GET", "/.well-known/ard.json", "local-admin")
    require(after[0] == 200 and after[2] == before[2] and header(after[1], "ETag") == header(before[1], "ETag"))
    return {"admission": admission, "entryDigest": record["versionRevision"]["contentSetDigest"],
            "opinions": opinions, "sharing": {"id": choice["id"], "etag": header(headers, "ETag")}}


def capture(journey, seed):
    """Check independent expected values before recording exact private readbacks."""
    snapshot = read_snapshot(journey, seed)
    path = journey.state / "rating-expectations.json"
    write_document(path, snapshot)
    require(path.stat().st_mode & 0o777 == 0o600)
    return snapshot


def read_snapshot(journey, seed):
    """Read current API and SQL state without replacing the source expectations."""
    admission = seed["admission"]
    require(set(seed["opinions"]) == set(OWNERS)
            and len({value["id"] for value in seed["opinions"].values()}) == 4)
    opinions, views = {}, {}
    for label, token in OWNERS.items():
        owner = Client(journey.base, token)
        expected = seed["opinions"][label]
        opinion, headers = owner.request("GET", "/api/v1/ratings/" + expected["id"])
        score, consent, state = HISTORY[label][-1]
        require(opinion["id"] == expected["id"] and opinion["revision"] == len(HISTORY[label]) == expected["revision"]
                and opinion["tenantId"] == TENANT and opinion["target"] == admission["target"]
                and opinion["releaseId"] == admission["releaseId"] and opinion["entryDigest"] == seed["entryDigest"]
                and (opinion["score"], opinion["consent"], opinion["state"]) == (score, consent, state)
                and header(headers, "ETag") == expected["etag"]
                and header(headers, "Cache-Control") == "no-store")
        opinions[label] = {"record": opinion, "etag": header(headers, "ETag")}
        view, _ = owner.request("POST", "/api/v1/ratings/read", admission)
        require(view["target"] == admission["target"] and view["count"] == 2 and view["average"] == 3.5
                and view["own"] == opinion and view["changedEntry"] is False)
        views[label] = view
    sharing, headers = journey.api.request("GET", "/api/v1/ratings/sharing?host=127.0.0.1&audience=anonymous")
    require(sharing["id"] == seed["sharing"]["id"] and sharing["tenantId"] == TENANT
            and sharing["host"] == "127.0.0.1" and sharing["audience"] == "anonymous"
            and sharing["enabled"] is True and sharing["revision"] == 1
            and header(headers, "ETag") == seed["sharing"]["etag"])
    tables = read_tables(journey)
    validate_retained(seed, opinions, sharing, tables)
    return {"admission": admission, "opinions": opinions, "views": views,
            "sharing": {"record": sharing, "etag": header(headers, "ETag")}, "tables": tables,
            "expectedLocal": {"count": 2, "sum": 7, "average": 3.5},
            "expectedShared": {"count": 1, "sum": 5, "summaryEligible": False}}


def read_tables(journey, history_count=7):
    tables = {}
    # Owner reads stay within the existing disposable database. LIMIT also
    # detects unexpected extra rows; none of these statements mutates SQL data.
    for table, (order, count) in TABLES.items():
        if table == "item_rating_revisions":
            count = history_count
        rows = json.loads(journey.sql("SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY " + order + "),'[]') "
            "FROM (SELECT * FROM " + table + " WHERE tenant_id='" + TENANT + "' ORDER BY " + order + " LIMIT 11) v"))
        require(isinstance(rows, list) and len(rows) == count and all(row["tenant_id"] == TENANT for row in rows))
        tables[table] = rows
    return tables


def digest(value):
    """Compare exact JSON values, including the distinction between booleans and integers."""
    return "sha256:" + hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()).hexdigest()


def verify_owner(journey, seed, expected, expected_digest):
    """Reject missing or changed expectations before any restored API can start."""
    path = journey.state / "rating-expectations.json"
    try:
        require(path.stat().st_mode & 0o777 == 0o600)
        require(digest(read_document(path)) == expected_digest == digest(expected))
        require(digest(expected["admission"]) == digest(seed["admission"]))
        require(set(expected["opinions"]) == set(OWNERS) and set(expected["views"]) == set(OWNERS))
        for label in OWNERS:
            opinion = expected["opinions"][label]
            require(opinion["record"]["id"] == seed["opinions"][label]["id"]
                    and opinion["record"]["revision"] == len(HISTORY[label])
                    and opinion["etag"] == seed["opinions"][label]["etag"])
        require(expected["expectedLocal"] == {"count": 2, "sum": 7, "average": 3.5}
                and expected["expectedShared"] == {"count": 1, "sum": 5, "summaryEligible": False})
        for table, (_, count) in TABLES.items():
            require(len(expected["tables"][table]) == count)
        validate_retained(seed, expected["opinions"], expected["sharing"]["record"], expected["tables"])
        require(digest(read_tables(journey)) == digest(expected["tables"]))
    except (OSError, KeyError, IndexError, TypeError, ValueError):
        raise Failure("backup-rating-expectations-invalid") from None
    return True


def later_changes(journey, seed):
    """Change the source after capture; preserve the protected recovery point."""
    journey.stopped("api", "worker", "peer-archives")
    journey.compose("up", "--no-deps", "--detach", "--wait", "--wait-timeout", "60", "api")
    commands = (("consented", "withdraw", None), ("private", "consent", None),
                ("withdrawn", "remove", journey.api))
    later = {}
    for label, action, moderator in commands:
        owner = Client(journey.base, OWNERS[label])
        old = seed["opinions"][label]
        if action == "consent":
            receipt, headers = mutate(owner, "POST", "/api/v1/ratings", {
                **seed["admission"], "score": 2, "consent": True}, etag=old["etag"])
            wanted = (2, True, "active")
        else:
            body = {"reason": "Owned post-backup moderation"} if moderator is not None else {}
            receipt, headers = mutate(moderator if moderator is not None else owner, "POST",
                "/api/v1/ratings/" + old["id"] + ":" + action, body, etag=old["etag"])
            wanted = (0, False, "removed" if action == "remove" else "withdrawn")
        require(receipt == {"id": old["id"], "revision": old["revision"] + 1})
        current, reply = owner.request("GET", "/api/v1/ratings/" + old["id"])
        require(current["revision"] == receipt["revision"]
                and (current["score"], current["consent"], current["state"]) == wanted
                and header(reply, "ETag") == header(headers, "ETag"))
        later[label] = current
    sharing, _ = mutate(journey.api, "POST", "/api/v1/ratings/sharing", {
        "host": "127.0.0.1", "audience": "anonymous", "enabled": False,
        "reason": "Owned post-backup sharing change"}, etag=seed["sharing"]["etag"])
    require(sharing["id"] == seed["sharing"]["id"] and sharing["revision"] == 2 and sharing["enabled"] is False)
    stored = read_tables(journey, history_count=10)
    require(all(stored["rating_targets"][0][key] == value for key, value in {
        "vote_count": 1, "score_sum": 2, "shared_count": 1, "shared_sum": 2}.items()))
    public = audit.request(journey.base, "GET", "/.well-known/ard.json", "local-admin")
    require(public[0] == 200 and public[2] == journey.public[2]
            and header(public[1], "ETag") == header(journey.public[1], "ETag"))
    journey.compose("stop", "--timeout", "20", "api")
    journey.stopped("api", "worker", "peer-archives")
    return {"opinions": later, "sharing": sharing}


def verify_api(journey, seed, expected, expected_digest, later):
    """Require the older recovery point through current authorized API reads."""
    verify_owner(journey, seed, expected, expected_digest)
    current = read_snapshot(journey, seed)
    require(digest(current) == expected_digest)
    require(set(later["opinions"]) == {"consented", "private", "withdrawn"})
    stranger = Client(journey.base, "local-template-reader")
    for label in OWNERS:
        opinion = expected["opinions"][label]["record"]
        path = "/api/v1/ratings/" + opinion["id"]
        moderator, headers = journey.api.request("GET", path)
        require(digest(moderator) == digest(opinion) and header(headers, "ETag") == expected["opinions"][label]["etag"])
        stranger.request("GET", path, expected=(404,))
        journey.other.request("GET", path, expected=(404,))
        if label in later["opinions"]:
            changed = later["opinions"][label]
            require(changed["id"] == opinion["id"] and changed["revision"] == opinion["revision"] + 1
                    and digest(changed) != digest(opinion))
    require(later["sharing"]["id"] == expected["sharing"]["record"]["id"]
            and later["sharing"]["revision"] == 2 and later["sharing"]["enabled"] is False)


def validate_retained(seed, opinions, sharing, tables):
    """Require nonempty history and exact totals, not equality with an empty dump."""
    target = tables["rating_targets"][0]
    require(target["target"] == seed["admission"]["target"]
            and target["resource_id"] == seed["admission"]["target"]["resourceId"]
            and target["version_revision_id"] == seed["admission"]["target"]["versionRevisionId"]
            and all(target[name] == value for name, value in TOTALS.items()))
    current = {row["id"]: row for row in tables["item_ratings"]}
    history = {(row["rating_id"], row["revision"]): row for row in tables["item_rating_revisions"]}
    require(len(current) == 4 and len(history) == 7
            and len({row["principal_key"] for row in current.values()}) == 4
            and len({row["submitted_by"] for row in current.values()}) == 4)
    for label, expected in seed["opinions"].items():
        opinion = opinions[label]["record"]
        row = current.get(expected["id"])
        require(row is not None and row["target_key"] == target["target_key"]
                and row["release_id"] == seed["admission"]["releaseId"] and row["entry_digest"] == seed["entryDigest"]
                and (row["score"], row["consent"], row["state"], row["revision"]) ==
                    (opinion["score"], opinion["consent"], opinion["state"], opinion["revision"])
                and datetime.datetime.fromisoformat(row["created_at"]) == datetime.datetime.fromisoformat(opinion["createdAt"])
                and datetime.datetime.fromisoformat(row["updated_at"]) == datetime.datetime.fromisoformat(opinion["updatedAt"]))
        for revision, values in enumerate(HISTORY[label], 1):
            retained = history.get((expected["id"], revision))
            require(retained is not None and (retained["score"], retained["consent"], retained["state"]) == values
                    and retained["entry_digest"] == seed["entryDigest"] and bool(retained["recorded_at"]))
        require(retained["recorded_at"] == row["updated_at"])
    saved = tables["rating_sharing"][0]
    require(all(saved[key] == sharing[field] for key, field in (
        ("id", "id"), ("tenant_id", "tenantId"), ("host", "host"), ("audience", "audience"),
        ("enabled", "enabled"), ("revision", "revision")))
        and datetime.datetime.fromisoformat(saved["updated_at"]) == datetime.datetime.fromisoformat(sharing["updatedAt"]))
