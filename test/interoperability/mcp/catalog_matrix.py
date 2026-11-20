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
        # Suppress raw request logging; the harness records bounded outcomes.
        pass

    def exchange(self):
        require(self.path in ("/mcp", "/ard/v1/mcp", "/ard/v1/search", "/.well-known/oauth-protected-resource/ard/v1/mcp") or self.path.startswith("/api/v1/"), "fixed proxy route")
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



def ard_envelope(result):
    value = result.model_dump(by_alias=True, exclude_none=True)
    require(not value.get("isError"), "ARD tool refusal")
    require(len(value["content"]) == 1 and value["content"][0]["type"] == "text", "ARD text shape")
    parsed = json.loads(value["content"][0]["text"])
    require(parsed == value["structuredContent"] and isinstance(parsed["results"], list), "complete ARD representations")
    require("data" not in parsed and "catalog/ard-search" in value["_meta"], "ARD crossed Control plane or lost HTTP metadata")
    require(TOKEN not in json.dumps(value), "ARD credential reflection")
    return parsed, value["_meta"]["catalog/ard-search"]


async def ard_checks():
    global PHASE
    rows = []
    async with AsyncExitStack() as stack:
        clients, anonymous_clients = [], []
        for protocol in ("2025-11-25", "2026-07-28"):
            PHASE = "http/" + protocol + "/ard-publication"
            http = await stack.enter_async_context(httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=20, headers={"Authorization": "Bearer " + TOKEN}))
            connector = streamable_http_client("https://localhost:9555/ard/v1/mcp", http_client=http, terminate_on_close=False)
            client = await stack.enter_async_context(Client(connector, mode="legacy" if protocol == "2025-11-25" else "auto", cache=None))
            clients.append(client)
            require(client.protocol_version == protocol and client.server_info.name == "open-agentic-catalog-ard", "ARD native negotiation")
            tools = (await client.list_tools()).tools
            require([t.name for t in tools] == ["search"], "ARD static inventory")
            before = await peers()
            query = dict(query=dict(text="demo"), federation="none", pageSize=2)
            actual, cursor = [], None
            for _ in range(20):
                args = dict(query, **({"pageToken": cursor} if cursor else {}))
                page, meta = ard_envelope(await client.call_tool("search", args))
                require(meta["partial"] is False, "local ARD completeness")
                actual += page["results"]
                # Compare the exact selected response through real REST with the
                # same host, actor, generation, filters and continuation.
                rest = await http.post("https://localhost:9555/ard/v1/search", json=args)
                require(rest.status_code == 200, "REST ARD control failed")
                direct = rest.json()
                require(bool(direct.get("pageToken")) == bool(page.get("pageToken")), "REST/MCP continuation mismatch")
                require({k: v for k, v in direct.items() if k != "pageToken"} == {k: v for k, v in page.items() if k != "pageToken"}, "REST/MCP ARD mismatch")
                cursor = page.get("pageToken")
                if not cursor:
                    break
            require(len(actual) > 2 and len({r["identifier"] for r in actual}) == len(actual), "ARD page duplication or vacuous corpus")
            expected = {r["identifier"] for r in EXPECTED["releases"]}
            require({r["identifier"] for r in actual} <= expected, "ARD returned a private or foreign entry")
            require({"application/a2a-agent-card+json", "application/mcp-server-card+json"} <= {r.get("type") for r in actual}, "nonempty Agent/MCP control")
            typed_query = dict(query={"text": "demo", "@context": {"kind": "https://agenticresourcediscovery.org/ns#mediaType"}, "filter": {"kind": "application/mcp-server-card+json"}}, federation="none", pageSize=100)
            typed, _ = ard_envelope(await client.call_tool("search", typed_query))
            require(typed["results"] and {r["identifier"] for r in typed["results"]} == {r["identifier"] for r in actual if r.get("type") == "application/mcp-server-card+json"}, "ARD inline context/filter lost membership")
            require(await peers() == before, "federation none contacted a peer")
            for bad in (dict(text="demo"), dict(query=dict(text="demo"), url="https://never-invoke.invalid")):
                refused = (await client.call_tool("search", bad)).model_dump(by_alias=True, exclude_none=True)
                require(refused.get("isError") is True and "structuredContent" not in refused, "ARD closed arguments")
            denial_http = await stack.enter_async_context(httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=20))
            for token, status in (("", 401), (Path("/catalog/other-tenant-reader-token").read_text(), 403)):
                denied = await denial_http.post("https://localhost:9555/ard/v1/mcp", headers={"Authorization": "Bearer " + token} if token else {}, json={"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
                require(denied.status_code == status, "ARD host authority: expected " + str(status) + ", got " + str(denied.status_code))
            # Anonymous access stays anonymous even with an invalid bearer.
            anonymous_http = await stack.enter_async_context(httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=20, headers={"Authorization": "Bearer invalid"}))
            anonymous_connector = streamable_http_client("https://127.0.0.1:9555/ard/v1/mcp", http_client=anonymous_http, terminate_on_close=False)
            anonymous = await stack.enter_async_context(Client(anonymous_connector, mode="legacy" if protocol == "2025-11-25" else "auto", cache=None))
            anonymous_clients.append(anonymous)
            anonymous_page, _ = ard_envelope(await anonymous.call_tool("search", query))
            require(anonymous_page["results"], "anonymous MCP positive control")
            public = await http.post("https://127.0.0.1:9555/ard/v1/search", headers={"Authorization": "Bearer invalid"}, json=query)
            require(public.status_code == 200 and public.json()["results"], "anonymous ARD positive control")
            # Both approved peers are enabled at the end of the Control matrix;
            # one has an owned outage. Local results must survive that failure.
            partial, meta = ard_envelope(await client.call_tool("search", dict(query=dict(text="demo"), federation="auto", pageSize=100)))
            require(meta["partial"] is True and any(r["identifier"] in expected for r in partial["results"]), "ARD live failure erased healthy local results")
            cases = dict.fromkeys(("inventory", "publication-pages", "rest-equivalence", "closed-arguments", "host-admission", "live-partial"), True)
            rows.append(dict(protocol=protocol, cases=cases))
        # Referral participation is an independent administrator opt-in. Enable
        # it only after the healthy live controls, through the existing API.
        source_path = "/api/v1/federation/sources/" + EXPECTED["liveSources"][0]
        _, etag = await api(source_path)
        preview, _ = await api(source_path + "/change-previews", dict(intent="update", referrals=dict(enabled=True)))
        await api(source_path, dict(referrals=dict(enabled=True), changePreviewDigest=preview["digest"], reason="Qualify approved ARD MCP referrals"), etag=etag, method="PATCH")
        before_referrals = await peers()
        for client, row in zip(clients, rows, strict=True):
            referrals, _ = ard_envelope(await client.call_tool("search", dict(query=dict(text="demo"), federation="referrals", pageSize=100)))
            require(len(referrals.get("referrals", [])) == 1 and referrals["referrals"][0]["identifier"].endswith(":" + EXPECTED["liveSources"][0]), "approved ARD referrals absent or incorrect")
            row["cases"]["referrals"] = True
        require(await peers() == before_referrals, "referral listing contacted a peer")
        await api("/api/v1/publication:unpublish", dict(host="localhost", reason="Qualify current ARD MCP publication authority"))
        for client, row in zip(clients, rows, strict=True):
            refused = (await client.call_tool("search", dict(query=dict(text="demo"), federation="none"))).model_dump(by_alias=True, exclude_none=True)
            require(refused.get("isError") is True and "structuredContent" not in refused and refused["content"][0]["text"].startswith("publication_unavailable:"), "unpublished ARD fell back to Control")
            row["cases"]["unpublication"] = True
        selected = next(r for r in EXPECTED["releases"] if r["identifier"] == anonymous_page["results"][0]["identifier"])
        release_path = "/api/v1/listing-releases/" + selected["releaseId"]
        _, etag = await api(release_path)
        await api(release_path + ":revoke", dict(reason="Qualify ARD MCP safety withdrawal"), etag=etag)
        for client, row in zip(anonymous_clients, rows, strict=True):
            for args in (query, dict(query, pageToken=anonymous_page["pageToken"])):
                refused = (await client.call_tool("search", args)).model_dump(by_alias=True, exclude_none=True)
                require(refused.get("isError") is True and "structuredContent" not in refused and refused["content"][0]["text"].startswith("publication_unavailable:"), "withdrawn ARD publication remained readable")
            row["cases"]["safety-withdrawal"] = True
    return rows


async def checks():
    rows = []
    for transport in ("stdio", "http"):
        for protocol in ("2025-11-25", "2026-07-28"):
            rows.append(await matrix_case(transport, protocol))
    ard = await ard_checks()
    require(Proxy.no_sessions, "MCP session state appeared")
    final = await peers()
    final_static = await observations()
    require(final["credentials"] == final["unexpected"] == final_static["unexpected"] == final_static["credentials"] == 0, "peer credential/execution boundary")
    print(json.dumps(dict(client="mcp@2.3.0", matrix=rows, ard=ard, noSessions=True, peerSafe=True, childrenExited=True)))


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
