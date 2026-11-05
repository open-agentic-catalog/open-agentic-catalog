"""Owned inert Python MCP fixture; packaged into a local dependency-free wheel."""
import json
import os
import sys


def main():
    if sys.argv[1:] != ["--literal=$(never-run);literal"] or os.getenv("CATALOG_MCP_SENTINEL_SECRET"):
        raise SystemExit(2)
    marker = os.open(os.environ["OWNED_RUNNER_SENTINEL"], os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    os.write(marker, b"started\n")
    for line in sys.stdin:
        request = json.loads(line)
        if "id" not in request:
            continue
        method = request["method"]
        if method == "initialize":
            result = {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "owned-uvx", "version": "1.0.0"}}
        elif method == "tools/list":
            result = {"tools": [{"name": "owned_uvx_echo", "description": "Owned inert uvx tool", "inputSchema": {"type": "object", "properties": {}}, "annotations": {"readOnlyHint": True}}]}
        elif method == "tools/call":
            os.write(marker, b"called\n")
            result = {"content": [{"type": "text", "text": "owned uvx native invocation; literal arguments preserved"}]}
        else:
            continue
        print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}), flush=True)
