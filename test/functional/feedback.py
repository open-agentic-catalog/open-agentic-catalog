"""Private feedback qualification through the real packaged API and its replay store."""

from federation import Client, key
from journeys import header


def run(api, other, base, seed):
    """Exercise exact release context, ownership, lifecycle, replay and unchanged publication."""
    reader = Client(base, "local-reader")
    resource_id = seed["resource"]["id"]
    _, resource_headers = reader.request("GET", "/api/v1/resources/" + resource_id)
    payload = {"resourceId": resource_id, "releaseId": seed["release"]["id"], "category": "documentation", "comment": "Synthetic private feedback"}
    command_headers = {"Idempotency-Key": key("feedback-create")}
    receipt, _ = reader.request("POST", "/api/v1/feedback", payload, (201,), command_headers)
    assert set(receipt) == {"id", "revision"}, "feedback receipt retained private data"
    path = "/api/v1/feedback/" + receipt["id"]
    current, headers = reader.request("GET", path)
    assert current["comment"] == payload["comment"] and current["versionRevisionId"] == seed["versionRevision"]["id"], "feedback context or comment mismatch"
    other.request("GET", path, expected=(404,))
    reader.request("GET", "/api/v1/feedback/inbox", expected=(403,))
    own, _ = reader.request("GET", "/api/v1/feedback?resourceId=" + resource_id)
    inbox, _ = api.request("GET", "/api/v1/feedback/inbox?resourceId=" + resource_id)
    assert any(item["id"] == receipt["id"] for item in own["items"]), "own feedback missing"
    assert any(item["id"] == receipt["id"] for item in inbox["items"]), "inbox feedback missing"

    def change(client, method, suffix, body, etag):
        """Require text-free exact-key replay for every feedback mutation."""
        request_headers = {"Idempotency-Key": key("feedback-change"), "If-Match": etag}
        result, response_headers = client.request(method, path + suffix, body, headers=request_headers)
        replay, replay_headers = client.request(method, path + suffix, body, headers=request_headers)
        assert result == replay and set(result) == {"id", "revision"}, "feedback command replay mismatch"
        assert header(replay_headers, "Idempotency-Replayed") == "true", "feedback replay marker missing"
        return header(response_headers, "ETag")

    etag = change(api, "POST", ":address", {"response": "Synthetic private response"}, header(headers, "ETag"))
    reader.request("PATCH", path, {"category": "other", "comment": "Stale edit"}, (412,), {"Idempotency-Key": key("feedback-stale"), "If-Match": header(headers, "ETag")})
    etag = change(api, "POST", ":reopen", {}, etag)
    etag = change(reader, "PATCH", "", {"category": "suggestion", "comment": "Revised feedback"}, etag)
    change(reader, "POST", ":withdraw", {}, etag)
    current, _ = reader.request("GET", path)
    assert current["state"] == "withdrawn" and "comment" not in current and "response" not in current, "withdrawn text remains readable"
    replay, _ = reader.request("POST", "/api/v1/feedback", payload, (201,), command_headers)
    assert replay == receipt and "comment" not in replay, "old create replay exposed removed text"
    second, second_headers = reader.request("POST", "/api/v1/feedback", payload, (201,), {"Idempotency-Key": key("feedback-second")})
    path = "/api/v1/feedback/" + second["id"]
    change(api, "POST", ":remove", {}, header(second_headers, "ETag"))
    current, _ = reader.request("GET", path)
    assert current["state"] == "removed" and "comment" not in current, "administrator removal retained text"
    _, after_headers = reader.request("GET", "/api/v1/resources/" + resource_id)
    assert header(after_headers, "ETag") == header(resource_headers, "ETag"), "feedback changed resource revision"
