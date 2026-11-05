"""Independent SDK assertions for Catalog stdio; no SDK source or wire rewriting."""

import asyncio
from builtins import BaseExceptionGroup
import json
from pathlib import Path
import re
import signal
import ssl
import sys
import tempfile
import threading
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from mcp_diagnostics import ToolRefusal

import httpx2
from mcp import Client, StdioServerParameters
from mcp.client import stdio

TOKEN = Path("/catalog/reader-token").read_text()
ADMIN = Path("/catalog/local-admin-token").read_text()
CA = "/private/ca.pem"
EXPECTED = json.loads(Path("/catalog/expected.json").read_text())
SESSIONS = []
PHASE = "setup"
CASES = {}


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def envelope(result):
    value = result.model_dump(by_alias=True, exclude_none=True)
    if value.get("isError"):
        raise ToolRefusal(value.get("content"))
    require(len(value["content"]) == 1 and value["content"][0]["type"] == "text", "tool content shape")
    parsed = json.loads(value["content"][0]["text"])
    require(parsed == value["structuredContent"], "text and structured results differ")
    require(parsed["meta"]["requestId"] and parsed["data"] is not None, "Control envelope")
    require(not any(secret in json.dumps(parsed) for secret in (TOKEN, ADMIN)), "credential reflection")
    return parsed["data"]


async def call(client, name, arguments=None):
    return await client.call_tool("catalog_control_" + name, arguments or {}, read_timeout_seconds=10)


async def failure(client, name, arguments, code):
    value = (await call(client, name, arguments)).model_dump(by_alias=True, exclude_none=True)
    require(value.get("isError") is True and "structuredContent" not in value, "partial refusal data")
    require(len(value["content"]) == 1 and value["content"][0]["type"] == "text", "refusal content")
    text = value["content"][0]["text"]
    require(text.split(":", 1)[0] == code, "unexpected refusal classification: " + code)
    require(TOKEN not in text and ADMIN not in text, "refusal leaked a credential")


async def api(path, body=None, bearer=ADMIN, etag=None):
    import uuid
    require(path.startswith("/api/v1/"), "fixed API path")
    headers = {"Authorization": "Bearer " + bearer}
    if body is not None:
        headers.update({"Content-Type": "application/json", "Idempotency-Key": str(uuid.uuid4())})
    if etag:
        headers["If-Match"] = etag
    async with httpx2.AsyncClient(timeout=10, trust_env=False) as client:
        response = await client.request("POST" if body is not None else "GET", "http://127.0.0.1:8080" + path, headers=headers, json=body)
    require(response.status_code == 200, "owned permission command failed")
    return response.json(), response.headers.get("etag")


async def observations():
    async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=5) as client:
        response = await client.get("https://127.0.0.1:9443/observations")
    require(response.status_code == 200 and len(response.content) < 4096, "peer observations")
    return response.json()


class ObservedStdio:
    """Observe the SDK-owned child's exit, without changing its bytes or behavior.

    The factory observer is bound to Python SDK 2.3.0, as the previous TypeScript
    driver observed its private child handle. The original factory runs unchanged.
    """

    def __init__(self, origin="http://127.0.0.1:8080", token_file="/catalog/reader-token", args=()):
        self.params = StdioServerParameters(command="/commands/catalogctl", args=["mcp", "serve", "--base-url", origin,
                                           "--token-file", token_file, "--timeout", "10s", *args],
                                           env={"CATALOG_CONFIG_FILE": "/missing/PRIVATE_SENTINEL",
                                                "CATALOG_DATABASE_DSN": "postgres://PRIVATE_SENTINEL@invalid",
                                                "CATALOG_REMOTE_TOKEN": "IGNORED_PRIVATE_SENTINEL", "MCPGODEBUG": ""})
        self.log = tempfile.TemporaryFile(mode="w+")
        self.process = None

    async def __aenter__(self):
        factory = stdio._create_platform_compatible_process
        async def observe(*args, **kwargs):
            self.process = await factory(*args, **kwargs)
            return self.process
        self.context = stdio.stdio_client(self.params, errlog=self.log)
        try:
            with patch.object(stdio, "_create_platform_compatible_process", observe):
                return await self.context.__aenter__()
        except BaseException:
            self.log.close()
            raise

    async def __aexit__(self, *args):
        return await self.context.__aexit__(*args)


async def connect(**kwargs):
    transport = ObservedStdio(**kwargs)
    client = Client(transport, mode="legacy", cache=None)
    await client.__aenter__()
    session = {"client": client, "transport": transport, "closed": False}
    SESSIONS.append(session)
    require(client.protocol_version == "2025-11-25", "legacy protocol negotiation")
    require(client.server_info.name == "open-agentic-catalog-control", "server identity")
    return session


async def close(session, unsuccessful=False):
    await session["client"].__aexit__(None, None, None)
    process = session["transport"].process
    require(process.returncode == (1 if unsuccessful else 0), "stdio exit status or forced termination")
    log = session["transport"].log
    log.seek(0)
    text = log.read(8193)
    require(len(text) <= 8192 and not any(s in text for s in (TOKEN, ADMIN, "PRIVATE_SENTINEL")), "stdio diagnostic exposure")
    require(unsuccessful or not text, "unexpected stdio diagnostics")
    log.close()
    session["closed"] = True


async def pages(client, filters, count):
    rows, cursors, cursor = [], set(), None
    for _ in range(32):
        page = envelope(await call(client, "search", {**filters, "limit": 2, **({"cursor": cursor} if cursor else {})}))
        require(not page["partial"] and 0 < len(page["items"]) <= 2, "page completeness")
        rows.extend(page["items"])
        cursor = page.get("nextCursor")
        if not cursor:
            require(len(rows) == count and len({r["id"] for r in rows}) == count, "Search membership/duplicates")
            return rows
        require(cursor not in cursors, "cursor loop")
        cursors.add(cursor)
    raise AssertionError("Search continuation bound")


class Fault(BaseHTTPRequestHandler):
    mode = "valid"
    began = threading.Event()
    cancelled = threading.Event()

    def log_message(self, *_):
        pass

    def do_GET(self):
        require(self.path == "/api/v1/capabilities", "fault API path")
        require(self.headers.get("Authorization") == "Bearer " + TOKEN and not self.headers.get("Cookie"), "fault credential boundary")
        if Fault.mode == "slow":
            Fault.began.set()
            self.connection.settimeout(.1)
            for _ in range(150):
                try:
                    if not self.connection.recv(1):
                        Fault.cancelled.set()
                        break
                except TimeoutError:
                    continue
            return
        value = {"resourceKinds": ["agent", "mcp_server", "skill", "plugin"], "search": {}, "limits": {}}
        if Fault.mode == "invalid-shape":
            value["resourceKinds"] = None
        if Fault.mode == "padded":
            value["search"]["padding"] = "x" * 6000
        if Fault.mode == "reflection":
            value["limits"]["reflected"] = TOKEN
        body = json.dumps(value).replace(TOKEN, "".join("\\u%04x" % ord(c) for c in TOKEN)).encode()
        if Fault.mode == "malformed":
            body = b"{"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("X-Request-ID", "owned-fault")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


async def started():
    require(await asyncio.to_thread(Fault.began.wait, 3), "upstream request start deadline")


async def cancelled():
    require(await asyncio.to_thread(Fault.cancelled.wait, 3), "upstream request cancellation deadline")


def pending():
    Fault.began.clear()
    Fault.cancelled.clear()


async def checks(fault):
    global PHASE
    before = await observations()
    require(before["credentials"] == 0 and before["unexpected"] == 0, "initial peer boundary")
    PHASE = "initialize-list"
    main = await connect()
    client = main["client"]
    server = client.server_info.model_dump(by_alias=True, exclude_none=True)
    listed = await client.list_tools()
    require({t.name for t in listed.tools} == {"catalog_control_" + n for n in ("capabilities", "search", "release", "indexed_record", "explore", "access")}, "six static tools")
    for t in listed.tools:
        value = t.model_dump(by_alias=True, exclude_none=True)
        require(value["annotations"]["readOnlyHint"] and value["inputSchema"]["additionalProperties"] is False and value["outputSchema"], "static schemas and read-only hint")
    capabilities = envelope(await call(client, "capabilities"))
    require(set(capabilities["resourceKinds"]) == {"agent", "mcp_server", "skill", "plugin"}, "four kinds")
    access = envelope(await call(client, "access"))
    direct, _ = await api("/api/v1/me", bearer=TOKEN)
    for field in ("tenantId", "permissions", "availablePermissions", "authorizationRevision", "namespaceGrants", "nextCursor", "scopeEnforcement", "scopes"):
        require(access.get(field) == direct.get(field), "current access projection")
    require(access["availablePermissionsUse"] == "navigation_only" and not {"subject", "membershipId", "realmId", "actorType", "claims"}.intersection(access), "identity disclosure")
    CASES[PHASE] = True
    PHASE = "four-kind-pages"
    local = await pages(client, {}, 12)
    require(all(i["type"] == "local" for i in local), "local defaults")
    require({i["local"]["release"]["id"] for i in local} == {i["releaseId"] for i in EXPECTED["releases"]}, "governed release membership")
    for kind in capabilities["resourceKinds"]:
        rows = await pages(client, {"kinds": [kind]}, 3)
        require(all(i["kind"] == kind for i in rows), "kind filter")
    facets = envelope(await call(client, "explore", {"facets": [{"field": "kind", "limit": 4}]}))
    require(facets["matched"] == 12 and {b["value"]: b["count"] for b in facets["facets"]["kind"]["buckets"]} == dict.fromkeys(capabilities["resourceKinds"], 3), "four-kind facets")
    bounded = envelope(await call(client, "explore", {"facets": [{"field": "kind", "limit": 1}]}))
    require(bounded["matched"] == 12 and len(bounded["facets"]["kind"]["buckets"]) == 1 and bounded["facets"]["kind"]["buckets"][0]["count"] == 3 and bounded["facets"]["kind"]["otherCount"] == 9 and not bounded.get("nextCursor"), "bounded facets")
    selected = envelope(await call(client, "explore", {"query": {"kinds": ["plugin"]}, "facets": [{"field": "kind"}]}))
    require(selected["matched"] == 3 and selected["facets"]["kind"]["buckets"] == [{"value": "plugin", "count": 3}], "filtered facets")
    CASES[PHASE] = True
    PHASE = "release-projection"
    for item in local:
        release_id = item["local"]["release"]["id"]
        detail = envelope(await call(client, "release", {"releaseId": release_id}))
        record, _ = await api("/api/v1/catalog/resources/" + release_id, bearer=TOKEN)
        seed = next(r for r in EXPECTED["releases"] if r["releaseId"] == release_id)
        require(detail["release"]["id"] == release_id and detail["versionRevision"]["id"] == seed["versionRevisionId"], "sealed release identity")
        for section in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
            require(detail[section]["id"] == record[section]["id"], "retained identity projection")
        references = detail["representations"]
        require(references and len(references) == len(record["versionRevision"]["representations"]) and len({r["id"] for r in references}) == len(references), "representation reference membership")
        for ref in references:
            retained = next(r for r in record["versionRevision"]["representations"] if r["id"] == ref["id"])
            require(all(ref[field] == retained[field] for field in ("id", "role", "mediaType", "profileId", "contentDigest", "size")), "retained representation projection")
            require(all(ref.get(field, "") == retained.get(field, "") for field in ("artifactId", "url")), "retained handoff reference")
            require(re.fullmatch(r"sha256:[a-f0-9]{64}", ref["contentDigest"]) and "data" not in ref, "native content disclosure")
    CASES[PHASE] = True
    PHASE = "indexed-selection"
    indexed = await pages(client, {"origins": ["indexed"]}, 3)
    require({i["id"] for i in indexed} == {i["id"] for i in EXPECTED["indexedControl"]}, "indexed direct control")
    require({(i["versionPresent"], i.get("version", "")) for i in indexed} == {(False, ""), (True, "1.0.0"), (True, "2.0.0")}, "version presence")
    for item in indexed:
        require(item["type"] == "indexed" and EXPECTED["sourceId"] in item["indexed"]["sourceIds"], "indexed provenance")
        selector = {k: item[k] for k in ("identifier", "versionPresent", "version") if k in item}
        detail = envelope(await call(client, "indexed_record", selector))
        require(detail["id"] == item["id"] and detail["indexed"] == item["indexed"], "indexed detail")
    await failure(client, "indexed_record", {"identifier": indexed[0]["identifier"], "versionPresent": True, "version": ""}, "api_not_found")
    mixed = await pages(client, {"origins": ["local", "indexed"]}, 15)
    require({i["type"] for i in mixed} == {"local", "indexed"}, "mixed origins")
    CASES[PHASE] = True
    PHASE = "closed-arguments"
    await failure(client, "search", {"baseURL": "https://never-contact.invalid"}, "invalid_arguments")
    await failure(client, "release", {"releaseId": "../../private"}, "invalid_arguments")
    await failure(client, "indexed_record", {"identifier": indexed[0]["identifier"], "versionPresent": False, "version": ""}, "invalid_arguments")
    first = envelope(await call(client, "search", {"limit": 2}))
    await failure(client, "search", {"limit": 3, "cursor": first["nextCursor"]}, "api_failure")
    CASES[PHASE] = True
    PHASE = "denied-access"
    denied = await connect(token_file="/catalog/invalid-token")
    await failure(denied["client"], "search", {}, "api_denied")
    await close(denied)
    CASES[PHASE] = True
    PHASE = "tenant-isolation"
    other = await connect(token_file="/catalog/other-tenant-reader-token")
    require(not envelope(await call(other["client"], "search"))["items"], "foreign tenant results")
    empty = envelope(await call(other["client"], "explore", {"facets": [{"field": "kind"}]}))
    require(empty["matched"] == 0 and not empty["facets"]["kind"]["buckets"], "foreign facets")
    await failure(other["client"], "release", {"releaseId": EXPECTED["releases"][0]["releaseId"]}, "api_not_found")
    await close(other)
    CASES[PHASE] = True
    PHASE = "revoked-access"
    current, _ = await api("/api/v1/access/self?limit=100", bearer=TOKEN)
    require(len(current["items"]) == 3 and not current.get("nextCursor"), "complete reader development bundle")
    for row in current["items"]:
        assignment = row["assignment"]
        await api("/api/v1/access/assignments/" + assignment["id"] + ":revoke", {"reason": "Qualify current MCP permissions"}, etag='"rev-' + str(assignment["revision"]) + '"')
    revoked = envelope(await call(client, "search"))
    require(not revoked["items"] and revoked["durableTotal"] == 0 and not revoked.get("nextCursor"), "revoked collection")
    facets = envelope(await call(client, "explore", {"facets": [{"field": "kind"}]}))
    require(facets["matched"] == 0 and not facets["facets"]["kind"]["buckets"], "revoked facets")
    current_access = envelope(await call(client, "access"))
    require(not current_access["permissions"] and not current_access["availablePermissions"] and current_access["tenantId"] == access["tenantId"], "revoked access hints")
    for name, args, code in (("search", {"origins": ["indexed"]}, "api_denied"), ("search", {"limit": 2, "cursor": first["nextCursor"]}, "api_failure"),
                             ("release", {"releaseId": EXPECTED["releases"][0]["releaseId"]}, "api_denied"),
                             ("indexed_record", {k: indexed[0][k] for k in ("identifier", "versionPresent", "version") if k in indexed[0]}, "api_denied")):
        await failure(client, name, args, code)
    await close(main)
    CASES[PHASE] = True
    origin = "https://127.0.0.1:" + str(fault.server_port)
    tls_args = ("--ca-file", CA)
    fixture = await connect(origin=origin, args=(*tls_args, "--max-concurrency", "1"))
    PHASE = "tls"
    envelope(await call(fixture["client"], "capabilities"))
    untrusted = await connect(origin=origin)
    await failure(untrusted["client"], "capabilities", {}, "api_failure")
    await close(untrusted)
    CASES[PHASE] = True
    PHASE = "malformed-response"
    for mode, code in (("malformed", "api_failure"), ("invalid-shape", "invalid_response")):
        Fault.mode = mode
        await failure(fixture["client"], "capabilities", {}, code)
    Fault.mode = "valid"
    envelope(await call(fixture["client"], "capabilities"))
    CASES[PHASE] = True
    for PHASE, flag, bound, code in (("api-byte-limit", "--max-response-bytes", "512", "api_failure"), ("tool-byte-limit", "--max-tool-bytes", "2048", "output_limit")):
        limited = await connect(origin=origin, args=(*tls_args, flag, bound))
        envelope(await call(limited["client"], "capabilities"))
        Fault.mode = "padded"
        await failure(limited["client"], "capabilities", {}, code)
        Fault.mode = "valid"
        envelope(await call(limited["client"], "capabilities"))
        await close(limited)
        CASES[PHASE] = True
    PHASE = "credential-reflection"
    Fault.mode = "reflection"
    await failure(fixture["client"], "capabilities", {}, "invalid_response")
    Fault.mode = "valid"
    envelope(await call(fixture["client"], "capabilities"))
    CASES[PHASE] = True
    PHASE = "cancellation"
    Fault.mode = "slow"
    pending()
    waiting = asyncio.create_task(call(fixture["client"], "capabilities"))
    await started()
    await failure(fixture["client"], "capabilities", {}, "busy")
    CASES["concurrency"] = True
    waiting.cancel()
    try:
        await asyncio.wait_for(waiting, 3)
        raise AssertionError("cancelled call succeeded")
    except asyncio.CancelledError:
        pass
    await cancelled()
    Fault.mode = "valid"
    envelope(await call(fixture["client"], "capabilities"))
    CASES[PHASE] = True
    await close(fixture)
    for PHASE in ("eof", "termination"):
        session = await connect(origin=origin, args=tls_args)
        Fault.mode = "slow"
        pending()
        waiting = asyncio.create_task(call(session["client"], "capabilities"))
        await started()
        if PHASE == "termination":
            session["transport"].process.send_signal(signal.SIGTERM)
        await close(session, unsuccessful=PHASE == "termination")
        await cancelled()
        try:
            result = await asyncio.wait_for(waiting, 3)
            require(PHASE == "eof" and result.model_dump(by_alias=True).get("isError"), "stopped call succeeded")
        except Exception as error:
            require(not isinstance(error, (AssertionError, TimeoutError)), "stopped call did not finish safely")
        CASES[PHASE] = True
    require(await observations() == before, "metadata calls contacted a resource or refreshed a source")
    require(all(s["closed"] for s in SESSIONS), "stdio children not reaped")
    print(json.dumps({"protocol": "2025-11-25", "client": "mcp@2.3.0", "server": server,
                      "cases": CASES, "childrenExited": True, "peerUnchanged": True}))


async def main():
    fault = ThreadingHTTPServer(("127.0.0.1", 0), Fault)
    fault.daemon_threads = True
    trust = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    trust.load_cert_chain("/private/server.pem", "/private/server.key")
    fault.socket = trust.wrap_socket(fault.socket, server_side=True)
    thread = threading.Thread(target=fault.serve_forever, daemon=True)
    thread.start()
    try:
        # SDK contexts and their failure cleanup must stay in the same task.
        async with asyncio.timeout(150):
            await checks(fault)
    except BaseException as error:
        # No native exception, response, token or OAuth value enters the report.
        def describe(value):
            if isinstance(value, BaseExceptionGroup):
                return [item for child in value.exceptions for item in describe(child)]
            frames = [str(f.lineno) for f in traceback.extract_tb(value.__traceback__) if Path(f.filename).name in ("catalog_matrix.py", "catalog_client.py")]
            return [type(value).__name__ + ":" + ",".join(frames)]
        print("Independent MCP qualification failed at " + PHASE + " (" + ";".join(describe(error)) + ")", file=sys.stderr)
        raise SystemExit(1) from None
    finally:
        for session in reversed(SESSIONS):
            if not session["closed"]:
                try:
                    await session["client"].__aexit__(None, None, None)
                except Exception:
                    pass
                session["transport"].log.close()
        fault.shutdown()
        fault.server_close()
        thread.join(timeout=2)


if __name__ == "__main__":
    asyncio.run(main())
