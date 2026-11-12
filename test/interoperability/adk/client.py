"""Call unchanged ADK MCP tools deterministically; never create a model or runner."""

import asyncio
import importlib.metadata
import json
import os
from pathlib import Path
import socket
import ssl
import sys
import time

from adk_contract import CASES, TOOLS, check_network, check_release, check_search_item, envelope, refusal, require

ORIGIN = "https://127.0.0.1:9555"
PROGRESS = Path("/progress")


def checkpoint(report):
    """Atomically retain only case outcomes for interruption diagnostics."""
    phase = report["phase"]
    outcomes = {name: "passed" if report["cases"].get(name) is True else
                ("failed" if "failure" in report else "incomplete") if name == phase else "unrun"
                for name in sorted(CASES)}
    temporary = PROGRESS / "cases.tmp"
    temporary.write_text(json.dumps({"phase": phase, "cases": outcomes}) + "\n")
    temporary.replace(PROGRESS / "cases.json")


def exception_tree(error):
    """Walk bounded native exception causes without rendering private messages."""
    todo, seen = [error], set()
    while todo and len(seen) < 64:
        current = todo.pop()
        if id(current) in seen:
            continue
        seen.add(id(current))
        yield current
        todo.extend(e for e in (current.__cause__, current.__context__) if e is not None)
        if isinstance(current, BaseExceptionGroup):
            todo.extend(current.exceptions)


async def run(report):
    """Exercise native discovery, exact reads and refusals in one owned namespace."""
    require(os.environ.get("GOOGLE_API_USE_CLIENT_CERTIFICATE") == "false", "native Google credential discovery must be disabled")
    require(os.environ.get("OTEL_SDK_DISABLED") == "true", "native telemetry must be disabled")
    require(os.environ.get("OTEL_PROPAGATORS") == "none", "native trace propagation must be disabled")
    check_network({name: int((Path("/sys/class/net") / name / "flags").read_text(), 16) for _, name in socket.if_nameindex()})
    import httpx2
    from google.adk.agents.invocation_context import InvocationContext
    from google.adk.sessions import InMemorySessionService
    from google.adk.sessions.session import Session
    from google.adk.tools.mcp_tool import McpToolset
    from google.adk.tools.mcp_tool.mcp_session_manager import StreamableHTTPConnectionParams
    from google.adk.tools.tool_context import ToolContext

    fixture = json.loads(Path("/client/input.json").read_text())
    ctx = ToolContext(invocation_context=InvocationContext(invocation_id="owned-adk-call", session_service=InMemorySessionService(),
                      session=Session(id="owned", app_name="catalog-adk", user_id="synthetic", state={}, events=[], last_update_time=0)))
    cases, sessions = report["cases"], []
    report.update(adk=importlib.metadata.version("google-adk"), mcp=importlib.metadata.version("mcp"))

    def begin(name):
        report["phase"] = name
        checkpoint(report)

    def passed(name):
        cases[name] = True
        checkpoint(report)

    def connect(token, trusted=True):
        """Use supported native transport injection with explicit CA and no proxies."""
        trust = ssl.create_default_context(cafile="/client/ca.pem") if trusted else ssl.create_default_context()

        def factory(headers=None, timeout=None, auth=None):
            return httpx2.AsyncClient(headers=headers, timeout=timeout, auth=auth, verify=trust, trust_env=False, follow_redirects=False)

        toolset = McpToolset(connection_params=StreamableHTTPConnectionParams(url=ORIGIN + "/mcp",
                            headers={"Authorization": "Bearer " + token} if token else {}, timeout=5,
                            sse_read_timeout=10, httpx_client_factory=factory))
        sessions.append(toolset)
        return toolset

    async def observations():
        """Read independent HTTP observations without an ADK or Catalog credential."""
        async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile="/client/ca.pem"), trust_env=False, timeout=5) as client:
            response = await client.get(ORIGIN + "/adk-observations")
        require(response.status_code == 200 and len(response.content) < 8192, "owned traffic observations")
        return response.json()

    async def call(name, arguments):
        """Invoke the originally discovered native tool with fixed test arguments."""
        return await tools["catalog_control_" + name].run_async(args=arguments, tool_context=ctx)

    async def rejected_connection(token, *, trusted=True):
        """Require the intended native HTTP authentication or certificate failure."""
        before = await observations()
        toolset = connect(token, trusted)
        try:
            await toolset.get_tools()
        except Exception as error:
            if trusted:
                after = await observations()
                require(after["statuses"].get("401", 0) > before["statuses"].get("401", 0), "authentication guard not reached")
            else:
                require(any(isinstance(e, ssl.SSLCertVerificationError) for e in exception_tree(error)), "TLS failed for another reason")
                require(await observations() == before, "untrusted TLS reached HTTP")
        else:
            raise AssertionError("native ADK accepted a refused connection")

    try:
        begin("inventory")
        toolset = connect(fixture["reader"])
        listed = await toolset.get_tools()
        tools = {tool.name: tool for tool in listed}
        require(set(tools) == TOOLS and len(listed) == len(TOOLS), "native tool inventory")
        passed("inventory")
        begin("schemas")
        declarations = {name: tool._get_declaration().model_dump(exclude_none=True) for name, tool in tools.items()}
        originals = {name: tool.raw_mcp_tool.model_dump(mode="json", by_alias=True, exclude_none=True) for name, tool in tools.items()}
        report["schemas"] = {"server": originals, "adk": declarations}
        search_schema = declarations["catalog_control_search"].get("parameters_json_schema") or declarations["catalog_control_search"]["parameters"]
        release_schema = declarations["catalog_control_release"].get("parameters_json_schema") or declarations["catalog_control_release"]["parameters"]
        require({"text", "origins", "kinds", "tags", "limit", "cursor"} <= set(search_schema["properties"]), "native Search declaration lost fields")
        require(set(search_schema["properties"]["kinds"]["items"]["enum"]) == {"agent", "mcp_server", "skill", "plugin"}, "native kind enum changed")
        require(set(search_schema["properties"]["origins"]["items"]["enum"]) == {"local", "indexed", "live"}, "native origin enum changed")
        require(release_schema["required"] == ["releaseId"], "native exact-read requirement changed")
        original_search = originals["catalog_control_search"]["inputSchema"]
        require(original_search["additionalProperties"] is False and original_search["properties"]["limit"]["maximum"] == 100, "original Search schema changed")
        passed("schemas")
        begin("capabilities-access")
        caps = envelope(await call("capabilities", {}))
        access = envelope(await call("access", {}))
        require(set(caps["resourceKinds"]) == {"agent", "mcp_server", "skill", "plugin"} and access["permissions"], "native capabilities/access")
        passed("capabilities-access")
        begin("known-search")
        query = {"text": "ADK metadata control", "origins": ["local"], "tags": ["adk-native"], "kinds": ["mcp_server"], "limit": 2}
        page = envelope(await call("search", query))
        require(page["tier"] == "core" and page["partial"] is False and not page.get("nextCursor"), "native local Search completeness")
        require(len(page["items"]) == 1 and page["items"][0]["local"]["release"]["id"] == fixture["record"]["release"]["id"], "native Search membership")
        check_search_item(page["items"][0], fixture["record"])
        passed("known-search")
        begin("contradictory-filter")
        empty = envelope(await call("search", {**query, "kinds": ["skill"]}))
        require(empty["items"] == [] and empty["durableTotal"] == 0, "native kind filter ignored")
        passed("contradictory-filter")
        begin("exact-release")
        release_id = page["items"][0]["local"]["release"]["id"]
        detail = envelope(await call("release", {"releaseId": release_id}))
        check_release(detail, fixture["record"])
        passed("exact-release")
        begin("explore")
        facets = envelope(await call("explore", {"query": {"tags": ["adk-native"]}, "facets": [{"field": "kind", "limit": 4}]}))
        require(facets["matched"] == 1 and facets["facets"]["kind"]["buckets"] == [{"value": "mcp_server", "count": 1}], "native facet result")
        passed("explore")
        begin("closed-arguments")
        await_result = await call("search", {"baseURL": "https://never-contact.invalid"})
        refusal(await_result, "invalid_arguments")
        refusal(await call("search", {**query, "limit": 101}), "invalid_arguments")
        refusal(await call("release", {"releaseId": "../../private"}), "invalid_arguments")
        passed("closed-arguments")
        begin("missing-token")
        await rejected_connection("")
        passed("missing-token")
        begin("invalid-token")
        await rejected_connection(fixture["invalid"])
        passed("invalid-token")
        begin("foreign-tenant")
        foreign = connect(fixture["foreign"])
        other_tools = {tool.name: tool for tool in await foreign.get_tools()}
        foreign_result = await other_tools["catalog_control_release"].run_async(args={"releaseId": release_id}, tool_context=ctx)
        refusal(foreign_result, "api_not_found")
        require(release_id not in json.dumps(foreign_result), "foreign tenant disclosed a private identity")
        passed("foreign-tenant")
        begin("untrusted-ca")
        await rejected_connection(fixture["reader"], trusted=False)
        passed("untrusted-ca")
        begin("revoked-access")
        Path("/progress/revoke").write_text("revoke-reader\n")
        deadline = time.monotonic() + 30
        while not Path("/progress/continue").is_file():
            require(time.monotonic() < deadline, "owned revocation checkpoint deadline")
            await asyncio.sleep(.05)
        require(Path("/progress/continue").read_text() == "revoked\n", "owned revocation checkpoint value")
        revoked = envelope(await call("search", query))
        require(revoked["items"] == [] and revoked["durableTotal"] == 0, "native adapter retained revoked Search access")
        refusal(await call("release", {"releaseId": release_id}), "api_denied")
        passed("revoked-access")
        report["sameToolsetRevocation"] = True
        report["phase"] = "complete"
    finally:
        for current in reversed(sessions):
            await current.close()
        report["closed"] = True
        checkpoint(report)
    return report


def main():
    """Keep private native exceptions out of the retained public result stream."""
    report = {"adk": None, "mcp": None, "cases": {}, "closed": False, "sameToolsetRevocation": False, "phase": "setup", "schemas": {}}
    try:
        asyncio.run(asyncio.wait_for(run(report), timeout=150))
    except BaseException as error:
        report["failure"] = type(error).__name__
        # Only this fixed harness assertion identifies the negative calibration.
        # Never serialize arbitrary native exception messages or arguments.
        if isinstance(error, AssertionError) and error.args == ("native Search display changed",):
            report["failureCheck"] = "search-display"
        checkpoint(report)
        print(json.dumps(report))
        return 1
    print(json.dumps(report))
    return 0


if __name__ == "__main__":
    sys.exit(main())
