"""Independent response/state oracles for generated namespace histories."""

from datetime import datetime


def stored(value):
    """PostgreSQL timestamps have microsecond precision; replay remains byte-exact."""
    return {key: datetime.fromisoformat(item) if key in {"createdAt", "updatedAt"} else item for key, item in value.items()}


KINDS = ("change", "replay", "reuse", "stale", "reader", "tenant-read", "tenant-write")
ERRORS = {"reuse": (409, "IDEMPOTENCY_KEY_REUSED"), "stale": (412, "PRECONDITION_FAILED"),
          "reader": (403, "FORBIDDEN"), "tenant-read": (404, "NOT_FOUND"), "tenant-write": (404, "NOT_FOUND")}


def check_reply(kind, status, body, etag, replay=None):
    if kind in ERRORS:
        expected, code = ERRORS[kind]
        assert status == expected and body.get("code") == code, "incorrect sequence rejection"
    else:
        assert status == 200, "sequence command did not succeed"
        assert etag == '"rev-' + str(body["revision"]) + '"', "response revision/ETag mismatch"
        if kind == "replay":
            assert (body, etag) == replay, "replay changed the original response"


def check_state(kind, before, after, display_name=None):
    if kind != "change":
        assert after == before, "replay or rejected command changed state"
        return
    before, after = stored(before), stored(after)
    expected = {**before, "displayName": display_name, "revision": before["revision"] + 1}
    # A successful command owns the new timestamp; all other retained fields
    # are compared independently, including identity, tenant and description.
    assert after["updatedAt"] >= before["updatedAt"], "update time regressed"
    expected["updatedAt"] = after["updatedAt"]
    assert after == expected, "successful command changed unexpected state"
