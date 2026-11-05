"""Unmodified official SDK exchanges through stdio and finite HTTPS MCP."""

import asyncio
from builtins import BaseExceptionGroup
from contextlib import AsyncExitStack
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import sys
import threading
import traceback

from mcp_diagnostics import ToolRefusal, failure_evidence, read_failure

import httpx2
from mcp import Client
from mcp.client.streamable_http import streamable_http_client
from catalog_client import CA, EXPECTED, TOKEN, ObservedStdio, call, envelope, failure, api, observations, require

KINDS = {"agent", "mcp_server", "skill", "plugin"}
PHASE = "setup"


class Proxy(BaseHTTPRequestHandler):
    """TLS termination only; forward SDK bytes unchanged to alternate replicas."""
    port = 8080
    alternate = True
    seen = set()
    no_sessions = True

    def log_message(self, *_):
        pass

    def exchange(self):
        require(self.path == "/mcp" or self.path.startswith("/api/v1/"), "fixed proxy route")
        port = Proxy.port
        Proxy.seen.add(port)
        if Proxy.alternate:
            Proxy.port = 8081 if port == 8080 else 8080
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=20)
        try:
            connection.request(self.command, self.path, body, dict(self.headers))
            response = connection.getresponse()
            value = response.read(2*1024*1024 + 1)
            require(len(value) <= 2*1024*1024, "proxy bound")
            Proxy.no_sessions = Proxy.no_sessions and not response.getheader("Mcp-Session-Id") and not self.headers.get("Mcp-Session-Id")
            self.send_response(response.status)
            for key, field in response.getheaders():
                if key.lower() not in ("transfer-encoding", "connection", "content-length", "server", "date"):
                    self.send_header(key, field)
            self.send_header("Content-Length", str(len(value)))
            self.end_headers()
            self.wfile.write(value)
        finally:
            connection.close()

    do_GET = exchange
    do_POST = exchange
    do_DELETE = exchange


async def peers():
    async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=5) as client:
        response = await client.get("https://127.0.0.1:9443/matrix-observations")
    require(response.status_code == 200, "live observations")
    return response.json()


async def search_pages(client, filters, total, partial=False, limit=2):
    rows, seen, cursor = [], set(), None
    for _ in range(32):
        page = envelope(await call(client, "search", dict(filters, limit=limit, **({"cursor": cursor} if cursor else {}))))
        require(page["partial"] is partial and len(page["items"]) <= limit, "page completeness")
        rows += page["items"]
        cursor = page.get("nextCursor")
        if not cursor:
            require(len(rows) == total and len({r["id"] for r in rows}) == total, "page membership/duplicates: " + str(len(rows)) + "/" + str(total) + "/" + str(len({r["id"] for r in rows})) + ":" + json.dumps(page.get("diagnostics", []))[:200])
            return rows
        require(cursor not in seen, "continuation loop")
        seen.add(cursor)
    raise AssertionError("continuation bound")


async def restart(transport, protocol):
    stage = transport + "-" + protocol
    Path("/tmp/restart").write_text(stage)
    for _ in range(500):
        file = Path("/tmp/continued")
        if file.exists() and file.read_text() == stage:
            return
        await asyncio.sleep(.1)
    raise AssertionError("API restart acknowledgment deadline")


async def matrix_case(transport, protocol):
    global PHASE
    cases = {}
    partial_path = "/api/v1/federation/sources/" + EXPECTED["liveSources"][1]
    _, partial_etag = await api(partial_path)
    await api(partial_path + ":pause", {"reason": "Healthy live continuation control"}, etag=partial_etag)
    Proxy.port, Proxy.alternate, Proxy.seen = 8080, True, set()
    stdio_transport = None
    async with AsyncExitStack() as stack:
        if transport == "stdio":
            stdio_transport = ObservedStdio(origin="https://127.0.0.1:9555", args=("--ca-file", CA))
            connector = stdio_transport
        else:
            http = await stack.enter_async_context(httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=20, headers={"Authorization": "Bearer " + TOKEN}))
            connector = streamable_http_client("https://127.0.0.1:9555/mcp", http_client=http, terminate_on_close=False)
        client = await stack.enter_async_context(Client(connector, mode="legacy" if protocol == "2025-11-25" else "auto", cache=None))
        require(client.protocol_version == protocol and client.server_info.name == "open-agentic-catalog-control", "independent negotiation")
        PHASE = transport + "/" + protocol + "/inventory"
        listing = await client.list_tools()
        require({t.name for t in listing.tools} == {"catalog_control_" + n for n in ("capabilities", "search", "release", "indexed_record", "explore", "access")}, "static tool inventory")
        require(set(envelope(await call(client, "capabilities"))["resourceKinds"]) == KINDS, "resource kinds")
        cases["inventory"] = True
        PHASE = transport + "/" + protocol + "/local-pages"
        before = await observations()
        before_live = await peers()
        local = await search_pages(client, {}, 12)
        require({r["local"]["release"]["id"] for r in local} == {r["releaseId"] for r in EXPECTED["releases"]}, "sealed release inventory")
        require({r["kind"] for r in local} == KINDS and all(r["type"] == "local" for r in local), "default local origin")
        cases["local-pages"] = True
        PHASE = transport + "/" + protocol + "/filters"
        first = local[0]
        summary = first["local"]
        filters = [{"text": first["displayName"]}, {"namespaceId": summary["resource"]["namespaceId"]}, {"tags": [first["tags"][0]]}, {"publisher": first["publisher"]}, {"license": "Apache-2.0"}, {"visibility": summary["listingRevision"]["visibility"]}]
        filters += [{"kinds": [kind]} for kind in sorted(KINDS)]
        for selected in filters:
            PHASE = transport + "/" + protocol + "/filter-" + next(iter(selected))
            direct, _ = await api("/api/v1/catalog/search", dict(origins=["local"], **selected, limit=100), bearer=TOKEN)
            require(direct["items"], "vacuous positive filter")
            actual = await search_pages(client, selected, len(direct["items"]))
            require({r["id"] for r in actual} == {r["id"] for r in direct["items"]}, "Control filter mismatch")
        # Curated samples have no categories. Exercise this supported filter
        # with a deliberate contradiction rather than invent sample metadata.
        await search_pages(client, {"categories": ["no-match-control"]}, 0)
        cases["filters"] = True
        PHASE = transport + "/" + protocol + "/indexed"
        indexed = await search_pages(client, {"origins": ["indexed"]}, 3)
        require({r["id"] for r in indexed} == {r["id"] for r in EXPECTED["indexedControl"]}, "indexed inventory")
        require({(r["versionPresent"], r.get("version", "")) for r in indexed} == {(False, ""), (True, "1.0.0"), (True, "2.0.0")}, "indexed version identity")
        for row in indexed:
            detail = envelope(await call(client, "indexed_record", {k: row[k] for k in ("identifier", "versionPresent", "version") if k in row}))
            require(detail["id"] == row["id"] and detail["indexed"] == row["indexed"] and EXPECTED["sourceId"] in detail["indexed"]["sourceIds"], "indexed provenance")
        await failure(client, "indexed_record", {"identifier": indexed[0]["identifier"], "versionPresent": True, "version": ""}, "api_not_found")
        await search_pages(client, {"origins": ["local", "indexed"]}, 15)
        cases["indexed"] = True
        PHASE = transport + "/" + protocol + "/exact-release"
        for row in local:
            release = envelope(await call(client, "release", {"releaseId": row["local"]["release"]["id"]}))
            direct, _ = await api("/api/v1/catalog/resources/" + release["release"]["id"], bearer=TOKEN)
            for name in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
                require(release[name]["id"] == direct[name]["id"], "retained identity")
            require(len(release["representations"]) == len(direct["versionRevision"]["representations"]), "representation inventory")
            for ref in release["representations"]:
                original = next(r for r in direct["versionRevision"]["representations"] if r["id"] == ref["id"])
                require(all(ref[k] == original[k] for k in ("id", "role", "mediaType", "profileId", "contentDigest", "size")) and "data" not in ref, "immutable reference")
        cases["exact-release"] = True
        PHASE = transport + "/" + protocol + "/explore-access"
        facets = envelope(await call(client, "explore", {"facets": [{"field": "kind", "limit": 1}]}))
        require(facets["matched"] == 12 and facets["facets"]["kind"]["otherCount"] == 9, "bounded local facets")
        access = envelope(await call(client, "access", {"limit": 1}))
        require(access["availablePermissionsUse"] == "navigation_only" and len(access["namespaceGrants"]) == 1 and access.get("nextCursor"), "bounded self access")
        next_access = envelope(await call(client, "access", {"limit": 1, "cursor": access["nextCursor"]}))
        require(next_access["namespaceGrants"] != access["namespaceGrants"] and access["authorizationRevision"] == next_access["authorizationRevision"], "self access continuation")
        cases["explore-access"] = True
        PHASE = transport + "/" + protocol + "/empty-sparse"
        await search_pages(client, {"text": "no-match-control"}, 0)
        await search_pages(client, {"origins": ["indexed"], "kinds": ["agent"]}, 0)
        await search_pages(client, {"tags": ["no-match-control"]}, 0)
        cases["empty-sparse"] = True
        PHASE = transport + "/" + protocol + "/closed-arguments"
        for args in ({"baseURL": "https://never-invoke.invalid"}, {"limit": 101}, {"categories": [""]}):
            await failure(client, "search", args, "invalid_arguments")
        # Exercise diagnostics with an actual SDK refusal, without failing this matrix.
        refused = await call(client, "search", {"limit": 101})
        try:
            envelope(refused)
        except ToolRefusal as error:
            diagnostic = failure_evidence(error, PHASE)
            require(diagnostic["toolRefusal"] == "invalid_arguments" and
                    read_failure(json.dumps(diagnostic)) == diagnostic, "native refusal diagnostics")
        else:
            require(False, "invalid tool arguments unexpectedly succeeded")
        page = envelope(await call(client, "search", {"limit": 2}))
        await failure(client, "search", {"limit": 2, "kinds": ["agent"], "cursor": page["nextCursor"]}, "api_failure")
        await failure(client, "search", {"limit": 3, "cursor": page["nextCursor"]}, "api_failure")
        require(await observations() == before and await peers() == before_live, "ordinary metadata caused outbound work")
        cases["closed-arguments"] = True
        PHASE = transport + "/" + protocol + "/live"
        # First prove complete continuation with the healthy peer alone.
        live = await search_pages(client, {"text": "Owned", "origins": ["live"], "tags": ["matrix-live"]}, 4)
        require({r["kind"] for r in live} == KINDS and all(r["type"] == "live" for r in live), "four live kinds")
        require({r["identifier"] for r in live} == {"urn:air:catalog.example.com:matrix:" + kind for kind in ("agent", "mcp", "skill", "plugin")}, "live identities")
        selected = await search_pages(client, {"text": "Owned", "origins": ["live"], "license": "Apache-2.0", "categories": ["development"], "publisher": "catalog.example.com", "kinds": ["plugin"]}, 1)
        require(selected[0]["kind"] == "plugin" and selected[0]["live"]["sourceId"] == EXPECTED["liveSources"][0], "live filter/provenance")
        for contradiction in ({"license": "MIT"}, {"categories": ["no-match-control"]},
                              {"publisher": "another.example.com"}, {"tags": ["no-match-control"]}):
            await search_pages(client, {"text": "Owned", "origins": ["live"], **contradiction}, 0)
        for kind in sorted(KINDS):
            PHASE = transport + "/" + protocol + "/live-kind-" + kind
            selected_kind = await search_pages(client, {"text": "Owned", "origins": ["live"], "kinds": [kind]}, 1)
            require(selected_kind[0]["kind"] == kind, "live kind filter")
        cases["live"] = True
        durable, _ = await api("/api/v1/catalog/search", {"text": "Owned", "origins": ["local", "indexed"], "limit": 100}, bearer=TOKEN)
        await search_pages(client, {"text": "Owned", "origins": ["local", "indexed", "live"]}, len(durable["items"]) + 4)
        _, partial_etag = await api(partial_path)
        await api(partial_path + ":resume", {"reason": "Partial peer failure control"}, etag=partial_etag)
        await search_pages(client, {"text": "Owned", "origins": ["live"]}, 4, partial=True, limit=4)
        bounded = envelope(await call(client, "search", {"text": "Owned", "origins": ["live"], "limit": 2}))
        require(len(bounded["items"]) == 2 and bounded["partial"] and any(d["status"] == "live_results_limited" for d in bounded["diagnostics"]), "bounded live window")
        await search_pages(client, {"origins": ["live"], "text": "no-match-control"}, 0, partial=True)
        now = await peers()
        require(now["live"] > before_live["live"] and now["partial"] > before_live["partial"] and now["credentials"] == now["unexpected"] == 0, "explicit live only/no credentials")
        cases["partial"] = True
        PHASE = transport + "/" + protocol + "/replica-continuation"
        Proxy.alternate, Proxy.port = False, 8080
        original = envelope(await call(client, "search", {"limit": 2}))
        Proxy.port = 8081
        continued = envelope(await call(client, "search", {"limit": 2, "cursor": original["nextCursor"]}))
        require(not {r["id"] for r in original["items"]}.intersection(r["id"] for r in continued["items"]), "replica cursor duplicates")
        require(original["generation"] == continued["generation"] and continued["durableTotal"] == 12, "replica generation")
        cases["replica-continuation"] = True
        PHASE = transport + "/" + protocol + "/restart-continuation"
        # Keep this SDK client alive while the host restarts the primary API.
        await restart(transport, protocol)
        Proxy.port = 8080
        after = envelope(await call(client, "search", {"limit": 2, "cursor": original["nextCursor"]}))
        require([r["id"] for r in after["items"]] == [r["id"] for r in continued["items"]] and after["generation"] == original["generation"], "restart invalidated a valid cursor")
        envelope(await call(client, "capabilities"))
        cases["restart-continuation"] = True
        PHASE = transport + "/" + protocol + "/stale-cursor"
        stale = envelope(await call(client, "search", {"origins": ["indexed"], "limit": 2}))
        path = "/api/v1/federation/sources/" + EXPECTED["sourceId"]
        _, etag = await api(path)
        _, etag = await api(path + ":pause", {"reason": "Stale cursor qualification"}, etag=etag)
        await failure(client, "search", {"origins": ["indexed"], "limit": 2, "cursor": stale["nextCursor"]}, "api_failure")
        await api(path + ":resume", {"reason": "Restore source after qualification"}, etag=etag)
        await search_pages(client, {"origins": ["indexed"]}, 3)
        cases["stale-cursor"] = True
        replicas = sorted(Proxy.seen)
        require(replicas == [8080, 8081], "same client missed a replica")
    if stdio_transport:
        require(stdio_transport.process.returncode == 0, "stdio child exit")
        stdio_transport.log.seek(0)
        require(not stdio_transport.log.read(8193), "stdio diagnostics")
        stdio_transport.log.close()
    return dict(transport=transport, protocol=protocol, cases=cases, replicas=replicas)


async def checks():
    rows = []
    for transport in ("stdio", "http"):
        for protocol in ("2025-11-25", "2026-07-28"):
            rows.append(await matrix_case(transport, protocol))
    require(Proxy.no_sessions, "MCP session state appeared")
    final = await peers()
    final_static = await observations()
    require(final["credentials"] == final["unexpected"] == final_static["unexpected"] == final_static["credentials"] == 0, "peer credential/execution boundary")
    print(json.dumps(dict(client="mcp@2.3.0", matrix=rows, noSessions=True, peerSafe=True, childrenExited=True)))


async def main():
    server = ThreadingHTTPServer(("127.0.0.1", 9555), Proxy)
    server.daemon_threads = True
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain("/private/server.pem", "/private/server.key")
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        async with asyncio.timeout(175):
            await checks()
    except BaseException as error:
        evidence = failure_evidence(error, PHASE)
        if evidence:
            print(json.dumps(evidence))
        def describe(value):
            if isinstance(value, BaseExceptionGroup):
                return [item for child in value.exceptions for item in describe(child)]
            frames = [str(f.lineno) for f in traceback.extract_tb(value.__traceback__) if Path(f.filename).name in ("catalog_matrix.py", "catalog_client.py")]
            return [type(value).__name__ + ":" + ",".join(frames) + (":" + str(value)[:300] if isinstance(value, AssertionError) else "")]
        print("Independent MCP qualification failed at " + PHASE + " (" + ";".join(describe(error)) + ")", file=sys.stderr)
        raise SystemExit(1) from None
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


if __name__ == "__main__":
    asyncio.run(main())
