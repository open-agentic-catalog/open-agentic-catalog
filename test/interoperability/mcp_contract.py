"""Assertions for the selected real MCP control, independent of fixture replies."""

import json

MARKER = "catalog-mcp-echo-control"
CASES = ("initialize", "tools-list", "echo", "missing-key", "tool-error")


def client_config(key=None):
    return {"mcpServers": {"handoff": {"type": "http", "url": "https://127.0.0.1:9443/mcp",
            "headers": {} if key is None else {"X-MCP-Test-Key": key},
            "protocolEra": "legacy", "connectionTimeout": 10000, "requestTimeout": 10000, "roots": []}}}


def native_error(stderr):
    lines = stderr.strip().splitlines()
    assert lines, "missing native error envelope"
    return json.loads(lines[-1])["error"]["code"]


def validate_case(case, result):
    if case == "missing-key":
        assert result.code == 3, "missing key must retain native authentication failure"
        assert native_error(result.stderr) == "auth_required"
        assert not result.stdout.strip(), "denied connection must not return an MCP result"
        return {"exit": result.code, "outcome": "authentication-required"}
    if case == "tool-error":
        assert result.code == 5, "invalid echo arguments must retain native tool failure"
        assert native_error(result.stderr) == "tool_is_error"
        data = json.loads(result.stdout)["result"]
        assert data["isError"] is True
        assert len(data["content"]) == 1 and data["content"][0]["type"] == "text"
        text = data["content"][0]["text"]
        assert all(part in text for part in ("-32602", "tool echo", "expected string, received number", "at message")), "missing native argument validation evidence"
        return {"exit": result.code, "outcome": "tool-error"}
    assert result.code == 0, "native MCP command failed"
    payload = json.loads(result.stdout)
    data = payload["result"]
    if case == "initialize":
        assert data["protocolVersion"] == "2025-11-25"
        assert data["serverInfo"]["name"] == "mcp-servers/everything"
        assert data["serverInfo"]["version"] == "2.0.0"
        return {"exit": 0, "outcome": "connected", "protocolVersion": data["protocolVersion"], "serverInfo": data["serverInfo"]}
    if case == "tools-list":
        names = [tool["name"] for tool in data["tools"]]
        assert len(names) == len(set(names)) and "echo" in names
        echo = next(tool for tool in data["tools"] if tool["name"] == "echo")
        assert echo["inputSchema"]["properties"]["message"]["type"] == "string"
        assert "message" in echo["inputSchema"]["required"]
        return {"exit": 0, "outcome": "listed", "tools": sorted(names)}
    assert case == "echo"
    assert data.get("isError", False) is False
    assert data["content"] == [{"type": "text", "text": "Echo: " + MARKER}]
    return {"exit": 0, "outcome": "echoed"}


def validate_observations(seen):
    assert seen["denied"] > 0 and seen["forwarded"] > 0
    assert seen["authorization"] == 0 and seen["otherTools"] == 0
    assert seen["methods"]["tools/call"] == 2, "only valid and invalid echo calls are selected"
    assert seen["methods"]["tools/list"] >= 1
    assert seen["http"]["GET"] == 4, "every admitted client must open its SSE stream"
    assert seen["http"]["POST"] >= 13
    assert len(seen["initialize"]) == 4, "every admitted CLI process must initialize"
    for item in seen["initialize"]:
        assert item["protocolVersion"] == "2025-11-25"
        assert "sampling" not in item["capabilities"] and "elicitation" not in item["capabilities"]
        assert "roots" in item["capabilities"]


def complete(report):
    assert report["scope"] == "mcp-direct-control"
    assert set(report["cases"]) == set(CASES), "incomplete MCP report"
    expected = ("connected", "listed", "echoed", "authentication-required", "tool-error")
    for case, outcome in zip(CASES, expected, strict=True):
        assert report["cases"][case]["outcome"] == outcome
        assert report["cases"][case]["exit"] == {"missing-key": 3, "tool-error": 5}.get(case, 0)
    validate_observations(report["observations"])
    assert report["cleanup"] is True
