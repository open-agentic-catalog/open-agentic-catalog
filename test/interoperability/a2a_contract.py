"""Independent, dependency-free oracles for the narrow A2A 1.0 control."""

import hashlib
import json

URL = "https://127.0.0.1:9443/"
CARD_PATH = "/.well-known/agent-card.json"
MESSAGE = "Catalog local A2A control: punctuation <&> and Unicode café"
MESSAGE_ID = "catalog-a2a-control-message"
CONTEXT_ID = "catalog-a2a-control-context"
EXPECTED = "Hello, World! I have received your request (" + MESSAGE + ")"
CASES = ("card", "send", "readback", "selection-refusals", "traffic")


def interaction(governed=False):
    """Separate task contexts prevent a prior direct exchange from satisfying handoff."""
    suffix = "-governed" if governed else ""
    return MESSAGE + suffix, MESSAGE_ID + suffix, CONTEXT_ID + suffix


def select_card(card):
    """Reject ambiguity/fallback before the independent SDK can select a transport."""
    if card.get("supportedInterfaces") != [{"url": URL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}]:
        raise ValueError("A2A control requires the single owned JSONRPC 1.0 interface")
    if card.get("capabilities") != {} or card.get("securitySchemes") or card.get("securityRequirements"):
        raise ValueError("A2A control requires an anonymous, non-streaming public card")
    if card.get("defaultInputModes") != ["text/plain"] or card.get("defaultOutputModes") != ["text/plain"]:
        raise ValueError("unexpected A2A interaction modes")
    if not all(card.get(field) for field in ("name", "description", "version", "skills")):
        raise ValueError("incomplete deployment card")
    return hashlib.sha256(json.dumps(card, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def check_task(task, *, governed=False):
    """Require an actual completed task, exact artifact and original request identity."""
    message, message_id, context_id = interaction(governed)
    assert isinstance(task.get("id"), str) and task["id"]
    assert task.get("contextId") == context_id
    assert task["status"]["state"] == "TASK_STATE_COMPLETED"
    artifacts = task["artifacts"]
    assert len(artifacts) == 1 and artifacts[0]["artifactId"]
    assert artifacts[0]["parts"] == [{"text": "Hello, World! I have received your request (" + message + ")", "mediaType": "text/plain"}]
    history = task["history"]
    assert len(history) == 2
    assert history[1]["messageId"] and history[1]["role"] == "ROLE_AGENT"
    assert history[1]["parts"] == [{"text": "Processing request..."}]
    assert history[0]["messageId"] == message_id and history[0]["contextId"] == context_id
    assert history[0]["role"] == "ROLE_USER"
    assert history[0]["parts"] == [{"text": message, "mediaType": "text/plain"}]


def check_result(result, *, governed=False):
    assert set(result) == {"card", "card_sha256", "send", "readback", "selection_refusals"}
    assert result["card_sha256"] == select_card(result["card"])
    check_task(result["send"], governed=governed)
    # The task contains two known messages. Requesting one must actually truncate
    # history, keeping the newest message and every other retained task field.
    assert result["readback"] == dict(result["send"], history=result["send"]["history"][-1:]), "unexpected task or history projection"
    assert result["selection_refusals"] == ["version", "binding", "endpoint", "ambiguous"]


def check_traffic(observed, result, *, governed=False):
    """Assert the real HTTP/RPC inventory, including forbidden credential forwarding."""
    message, message_id, context_id = interaction(governed)
    assert len(observed) == (2 if governed else 3), "unexpected invocation, card refetch, streaming or polling"
    assert [(r["method"], r["path"], r["rpc"]) for r in observed] == ([] if governed else [("GET", CARD_PATH, None)]) + [("POST", "/", "SendMessage"), ("POST", "/", "GetTask")]
    for row in observed:
        assert row["authorization"] is False and row["cookie"] is False
        assert row["query"] is False and row["version"] == "1.0"
        assert row["status"] == 200
    send, get = observed[-2:]
    assert send["jsonrpc"] == get["jsonrpc"] == "2.0"
    assert send["request_id"] and get["request_id"] and send["request_id"] != get["request_id"]
    assert send["params"]["message"]["messageId"] == message_id
    assert send["params"]["message"]["contextId"] == context_id
    assert send["params"]["message"]["role"] == "ROLE_USER"
    assert send["params"]["message"]["parts"] == [{"text": message, "mediaType": "text/plain"}]
    assert send["params"]["configuration"] == {"acceptedOutputModes": ["text/plain"], "historyLength": 2}
    assert get["params"] == {"id": result["send"]["id"], "historyLength": 1}


def complete(report):
    """Never turn partial execution or failed cleanup into a passing control."""
    assert report["scope"] == "direct-control"
    assert report["cleanup"] and not report.get("creation_unconfirmed")
    assert set(report["cases"]) == set(CASES) and all(value is True for value in report["cases"].values())
    assert report["native_exit"] == 0
    check_result(report["result"])
    check_traffic(report["observations"], report["result"])
