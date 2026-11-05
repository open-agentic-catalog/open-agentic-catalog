"""Focused Catalog proxy qualification in the owned SDK client container."""

import asyncio
import copy
import json
from pathlib import Path
import time

if __package__:
    from .checks import tool_pages
    from .publication import publish
else:
    from checks import tool_pages
    from publication import publish

PROTOCOLS = ("2025-11-25", "2026-07-28")


async def dataset(c):
    card = json.loads(Path("/assets/agent.json").read_text())
    expected = {"a": [], "b": []}
    for org in ("a", "b"):
        admin = c.SECRETS["tokens"][org + "-admin"]
        publisher, etag = await c.request("/api/v1/publishers", {"displayName": "ContextForge " + org, "domains": ["catalog.example.com"]}, admin, return_headers=True)
        await c.request("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned proxy fixture"}, admin, etag=etag, expected=200)
        for number in range(2 if org == "a" else 1):
            value = copy.deepcopy(card)
            value.update(name="ContextForge " + org + " agent " + str(number), description="Inert Catalog proxy metadata.")
            value["supportedInterfaces"][0]["url"] = c.ORIGIN + "/never-invoke"
            expected[org].append(await publish(c, org, kind="agent", identifier="urn:air:catalog.example.com:contextforge:" + org + "-" + str(number), version="1.0.0",
                representations=[{"role": "primary", "mediaType": "application/a2a-agent-card+json", "data": value}], name=value["name"], tags=["contextforge", "tenant-" + org],
                publisher=publisher["id"], description="Inert governed Catalog metadata."))
    admin = c.SECRETS["tokens"]["a-admin"]
    inspection = await c.request("/api/v1/federation/source-inspections", {"url": c.ORIGIN + "/.well-known/ard.json", "networkProfile": "contextforge-owned"}, admin)
    c.require(inspection["entryCount"] == 1, "one indexed source fixture")
    source, etag = await c.request("/api/v1/federation/sources", {"inspectionId": inspection["id"], "displayName": "ContextForge indexed fixture", "retrievalMode": "indexed",
                                 "updateMode": "manual_review", "networkProfile": "contextforge-owned", "scope": {"publishers": ["catalog.example.com"]}, "reason": "Owned proxy fixture"}, admin, return_headers=True)
    receipt = await c.request("/api/v1/federation/sources/" + source["id"] + ":sync", {}, admin, etag=etag, expected=202)
    deadline = time.monotonic() + 40
    while True:
        job = await c.request("/api/v1/jobs/" + receipt["job"]["id"], token=admin)
        c.require(job["state"] not in ("failed", "cancelled"), "indexed fixture synchronization succeeds")
        if job["state"] == "succeeded":
            break
        c.require(time.monotonic() < deadline, "indexed fixture synchronization deadline")
        await asyncio.sleep(.1)
    snapshot = await c.request("/api/v1/federation/snapshots/" + job["result"]["snapshotId"], token=admin)
    c.require(snapshot["state"] == "complete" and snapshot["memberCount"] == 1 and snapshot["enumerationComplete"], "complete indexed fixture snapshot")
    expected["source"] = source["id"]
    c.save("dataset.json", expected)
    return {"aLocal": len(expected["a"]), "bLocal": len(expected["b"]), "indexed": snapshot["memberCount"], "independentReview": True}


class Proxy:
    def __init__(self, c):
        self.c = c
        self.private = json.loads(Path("/state/contextforge-connections.json").read_text())
        self.connections = self.private["connections"]
        self.expected = json.loads(Path("/state/dataset.json").read_text())
        self.identities = json.loads(Path("/state/catalog-identities.json").read_text())

    def connect(self, org="a", protocol="2026-07-28", *, extra=None, connection=None, headers=None, private_ca=True, responses=None):
        selected = connection or self.connections[org]
        values = headers if headers is not None else {"Authorization": "Bearer " + selected["token"]}
        return self.c.mcp(self.c.CF + "/servers/" + selected["server"] + "/mcp", values | (extra or {}), mode="legacy" if protocol == PROTOCOLS[0] else "auto", private_ca=private_ca, responses=responses)

    def tool(self, org, name):
        return next(t["name"] for t in self.connections[org]["selected"]["tools"] if t.get("originalName", t.get("original_name")) == "catalog_control_" + name)

    async def call(self, client, name, arguments=None, org="a"):
        return await client.call_tool(self.tool(org, name), arguments or {}, read_timeout_seconds=12)

    async def data(self, client, name, arguments=None, org="a"):
        return self.c.envelope(await self.call(client, name, arguments, org))

    async def observations(self):
        value = await self.c.request("/observations")
        self.c.require(value["overflow"] == 0, "complete bounded request observations")
        return value["events"]

    async def pages(self, client, query=None, org="a"):
        found, seen, cursor = [], set(), None
        for _ in range(8):
            page = await self.data(client, "search", dict(query or {}, limit=1, **({"cursor": cursor} if cursor else {})), org)
            self.c.require(page["partial"] is False and len(page["items"]) <= 1, "complete bounded Catalog page")
            found.extend(page["items"])
            cursor = page.get("nextCursor")
            if not cursor:
                self.c.require(len(found) == len({i["id"] for i in found}), "unique Catalog result pages")
                self.c.require(page["durableTotal"] == len(found), "Catalog count agrees with complete result pages")
                return found
            self.c.require(cursor not in seen, "Catalog cursor makes progress")
            seen.add(cursor)
        raise AssertionError("Catalog page bound")

    async def refusal(self, client, name, arguments, code=None, org="a"):
        try:
            result = await self.call(client, name, arguments, org)
        except Exception as error:
            leaves = exception_leaves(error)
            # Protocol refusal is distinct from connection failure or timeout.
            self.c.require(all(type(e).__name__ in ("MCPError", "McpError", "ValidationError") for e in leaves), "classified gateway protocol refusal")
            text = " ".join(str(e) for e in leaves)
            if code:
                self.c.require(code in text, "upstream refusal code survives gateway translation")
            return "rpc_error"
        value = result.model_dump(by_alias=True, exclude_none=True)
        self.c.require(value.get("isError") is True, "gateway refusal is not a successful empty result")
        text = " ".join(item.get("text", "") for item in value.get("content", []))
        if code:
            self.c.require(code in text, "upstream refusal code survives gateway translation")
        self.c.require("structuredContent" not in value, "refusal contains no successful data")
        return "tool_error"

    async def protocol(self, protocol):
        c = self.c
        c.PHASE = "proxy-" + protocol
        async with c.mcp(c.ORIGIN + "/mcp", {"Authorization": "Bearer " + c.SECRETS["tokens"]["a-reader"]}, mode="legacy" if protocol == PROTOCOLS[0] else "auto") as direct:
            native = {t.name: t.model_dump(by_alias=True, exclude_none=True) for t in (await direct.list_tools()).tools}
        before = await self.observations()
        async with self.connect(protocol=protocol) as client:
            c.require(client.protocol_version == protocol, "selected downstream protocol")
            listed = await tool_pages(client)
            selected = self.connections["a"]["selected"]["tools"]
            mapping = {t["name"]: t.get("originalName", t.get("original_name")) for t in selected}
            c.require(len(listed) == 6 and {t.name for t in listed} == set(mapping), "all six namespaced Catalog tools")
            for tool in listed:
                actual = tool.model_dump(by_alias=True, exclude_none=True)
                original = native[mapping[tool.name]]
                for key in ("inputSchema", "outputSchema", "annotations"):
                    c.require(actual.get(key) == original.get(key), "native tool schemas and annotations agree")
            c.require(set((await self.data(client, "capabilities"))["resourceKinds"]) == {"agent", "mcp_server", "skill", "plugin"}, "proxied capabilities")
            local = await self.pages(client)
            c.require({r["local"]["release"]["id"] for r in local} == {r["release"] for r in self.expected["a"]} and all(r["type"] == "local" for r in local), "default local Search and continuation")
            for query in ({"kinds": ["agent"]}, {"tags": ["contextforge"]}, {"namespaceId": self.expected["a"][0]["namespace"]},
                          {"license": "Apache-2.0"}, {"publisher": self.expected["a"][0]["publisher"]}):
                c.require({r["id"] for r in await self.pages(client, query)} == {r["id"] for r in local}, "positive Search filter")
            for query in ({"text": "no-contextforge-result"}, {"kinds": ["plugin"]}, {"tags": ["no-contextforge-tag"]}):
                c.require(not await self.pages(client, query), "successful empty Search")
            first = self.expected["a"][0]
            release = await self.data(client, "release", {"releaseId": first["release"]})
            direct = await c.request("/api/v1/catalog/resources/" + first["release"], token=c.SECRETS["tokens"]["a-reader"])
            for key in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
                c.require(release[key]["id"] == direct[key]["id"], "immutable release identity")
            for ref in release["representations"]:
                original = next(r for r in direct["versionRevision"]["representations"] if r["id"] == ref["id"])
                c.require(all(ref[k] == original[k] for k in ("id", "role", "mediaType", "profileId", "contentDigest", "size")), "immutable representation reference")
            indexed = await self.pages(client, {"origins": ["indexed"]})
            c.require(len(indexed) == 1 and indexed[0]["identifier"] == "urn:air:catalog.example.com:server:contextforge-indexed", "one indexed record")
            detail = await self.data(client, "indexed_record", {k: indexed[0][k] for k in ("identifier", "versionPresent", "version") if k in indexed[0]})
            c.require(detail["id"] == indexed[0]["id"] and detail["indexed"] == indexed[0]["indexed"] and self.expected["source"] in detail["indexed"]["sourceIds"], "indexed identity and provenance")
            c.require(len(await self.pages(client, {"origins": ["local", "indexed"]})) == 3, "mixed origin count")
            facets = await self.data(client, "explore", {"facets": [{"field": "kind"}]})
            c.require(facets["matched"] == 2 and facets["facets"]["kind"]["buckets"] == [{"value": "agent", "count": 2}], "authorized Explore counts")
            access = await self.data(client, "access")
            current = await c.request("/api/v1/me", token=c.SECRETS["tokens"]["a-reader"])
            c.require(access["tenantId"] == current["tenantId"] and access["authorizationRevision"] == current["authorizationRevision"] and access["namespaceGrants"] == current["namespaceGrants"], "current scoped access")
            argument_start = await self.observations()
            translated = await self.refusal(client, "capabilities", {"unexpected": True})
            argument_events = (await self.observations())[len(argument_start):]
            validation = "catalog" if any(e.get("tool") == "catalog_control_capabilities" for e in argument_events) else "gateway_or_client"
        events = (await self.observations())[len(before):]
        hops = sorted({e.get("negotiated") or e.get("protocol") for e in events if e["path"] == "catalog" and e.get("protocol")})
        c.require(hops and all(p in PROTOCOLS for p in hops), "observed upstream protocols")
        return {"protocol": protocol, "upstreamProtocols": hops, "inventory": True, "schemas": True, "sixTools": True, "pagesAndFilters": True,
                "emptyResults": True, "exactReferences": True, "indexed": True, "facetsAndAccess": True, "closedArguments": translated, "argumentValidation": validation}

    async def isolation_and_revocation(self):
        c = self.c
        c.PHASE = "proxy-isolation-revocation"
        async with self.connect() as client:
            await tool_pages(client)
            first = self.expected["a"][0]["release"]
            c.require((await self.data(client, "release", {"releaseId": first}))["release"]["id"] == first, "valid read before revocation")
            grant = self.identities["a"]["grant"]
            c.PHASE = "proxy-revoke-grant"
            await c.request("/api/v1/access/assignments/" + grant["id"] + ":revoke", {"reason": "Current scoped-read control"}, c.SECRETS["tokens"]["a-admin"], etag='"rev-' + str(grant["revision"]) + '"', expected=200)
            before = await self.observations()
            c.PHASE = "proxy-revoked-forwarded-read"
            code = await self.refusal(client, "release", {"releaseId": first}, "api_denied")
            c.PHASE = "proxy-revoked-direct-read"
            async with c.mcp(c.ORIGIN + "/mcp", {"Authorization": "Bearer " + c.SECRETS["tokens"]["a-reader"]}) as direct:
                result = await direct.call_tool("catalog_control_release", {"releaseId": first}, read_timeout_seconds=10)
                c.require(result.is_error and "api_denied" in result.content[0].text, "direct Catalog denial agrees")
            contacted = (await self.observations())[len(before):]
            c.require(len([e for e in contacted if e.get("tool") == "catalog_control_release" and e["credential"] == "a-reader"]) >= 2, "proxied and direct revoked reads reach Catalog with the unchanged token")
            c.PHASE = "proxy-revoked-collections"
            c.require(not await self.pages(client), "revoked namespace excluded from Search")
            facets = await self.data(client, "explore", {"facets": [{"field": "kind"}]})
            c.require(facets["matched"] == 0 and not facets["facets"]["kind"]["buckets"], "revoked namespace excluded from Explore")
            c.PHASE = "proxy-restore-grant"
            await c.request("/api/v1/role-assignments", {"subject": "a-reader", "actorType": "human", "role": "reader", "namespaceId": self.identities["a"]["namespace"], "reason": "Restore exact fixture read grant"}, c.SECRETS["tokens"]["a-admin"])
            c.require((await self.data(client, "release", {"releaseId": first}))["release"]["id"] == first and len(await self.pages(client)) == 2, "same open client and gateway recover after grant restoration")
        c.PHASE = "proxy-other-tenant"
        async with self.connect("b") as other:
            c.PHASE = "proxy-other-tenant-list"
            await tool_pages(other)
            c.PHASE = "proxy-other-tenant-search"
            rows = await self.pages(other, org="b")
            c.require({r["local"]["release"]["id"] for r in rows} == {r["release"] for r in self.expected["b"]}, "tenant B isolated results")
            c.PHASE = "proxy-other-tenant-exact-refusal"
            await self.refusal(other, "release", {"releaseId": self.expected["a"][0]["release"]}, "api_not_found", org="b")
        c.PHASE = "proxy-wrong-virtual-server"
        statuses = []
        await self.http_denial(self.connect(connection=dict(self.connections["a"], token=self.connections["b"]["token"]), responses=statuses), {403, 404}, observed=statuses)
        return {"twoTenants": True, "wrongTeam": True, "revokedExactRead": code, "upstreamContact": True, "revokedCollections": True, "sameClientRestored": True}

    async def http_denial(self, context, statuses=None, tls=False, observed=None):
        c = self.c
        try:
            async with context as client:
                await tool_pages(client)
        except Exception as error:
            leaves = exception_leaves(error)
            if tls:
                c.require(any("CERTIFICATE_VERIFY_FAILED" in str(e) for e in leaves), "actual certificate refusal")
            else:
                codes = {e.response.status_code for e in leaves if hasattr(e, "response")} | {code for code in observed or [] if code >= 400}
                c.require(codes and codes <= statuses, "actual HTTP admission refusal")
            return
        raise AssertionError("invalid connection unexpectedly succeeded")

    async def credential_controls(self):
        c = self.c
        c.PHASE = "proxy-credentials"
        for name, values, statuses in (("missing", {"headers": {}}, {401}), ("invalid", {"headers": {"Authorization": "Bearer invalid-contextforge"}}, {401}),
                                       ("host", {"extra": {"Host": "not-allowed.example:4444"}}, {403}), ("origin", {"extra": {"Origin": "https://not-allowed.example"}}, {403})):
            c.PHASE = "proxy-credentials-" + name
            observed = []
            await self.http_denial(self.connect(**values, responses=observed), statuses, observed=observed)
        await self.http_denial(self.connect(private_ca=False), tls=True)
        for label, status in (("a-expired", 401), ("a-audience", 401), ("a-scope", 403)):
            c.PHASE = "proxy-upstream-" + label
            async with self.connect(extra={"X-Upstream-Authorization": "Bearer " + c.SECRETS["tokens"][label]}) as client:
                await tool_pages(client)
                before = await self.observations()
                await self.refusal(client, "search", {})
                events = (await self.observations())[len(before):]
                c.require(any(e["credential"] == label and e["path"] == "catalog" for e in events), "override token reaches Catalog")
                if label == "a-scope":
                    c.require(any(e["credential"] == label and e.get("tool") == "catalog_control_search" for e in events), "scope refusal reaches the Catalog tool")
                c.require(any(e["credential"] == label and e["status"] == status for e in events), "actual upstream authentication or scope response")
            await c.request("/api/v1/me", token=c.SECRETS["tokens"][label], expected=200 if label == "a-scope" else status)
        c.PHASE = "proxy-upstream-direct-scope"
        direct_responses = []
        async with c.mcp(c.ORIGIN + "/mcp", {"Authorization": "Bearer " + c.SECRETS["tokens"]["a-scope"]}, responses=direct_responses) as direct:
            try:
                await direct.call_tool("catalog_control_search", {}, read_timeout_seconds=10)
            except Exception as error:
                c.require(all(type(e).__name__ in ("MCPError", "McpError", "HTTPStatusError") for e in exception_leaves(error)), "direct scope refusal uses the protocol boundary")
            else:
                raise AssertionError("direct MCP unexpectedly accepted an unrelated OAuth scope")
        c.require(403 in direct_responses, "direct Catalog scope admission agrees")
        c.PHASE = "proxy-upstream-valid-override"
        async with self.connect(extra={"X-Upstream-Authorization": "Bearer " + c.SECRETS["tokens"]["b-reader"]}) as client:
            await tool_pages(client)
            results = await self.pages(client)
            c.require({r["local"]["release"]["id"] for r in results} == {r["release"] for r in self.expected["b"]}, "valid explicit upstream header selects its own tenant")
        async with self.connect() as client:
            await tool_pages(client)
            access = await self.data(client, "access")
            a = await c.request("/api/v1/me", token=c.SECRETS["tokens"]["a-reader"])
            c.require(access["tenantId"] == a["tenantId"], "inbound ContextForge bearer does not replace Catalog bearer")
        return {"missingAndInvalidInbound": True, "host": True, "origin": True, "privateTLS": True, "expiredAudienceAndScope": True,
                "explicitOverride": True, "configuredIdentity": True, "upstreamDenials": {"expired": 401, "audience": 401, "scope": 403}}

    async def subset_and_rotation(self):
        c = self.c
        c.PHASE = "proxy-subset-rotation"
        connection = self.connections["a"]
        allowed = next(t for t in connection["selected"]["tools"] if t["name"] == self.tool("a", "capabilities"))
        admin = self.private["admin"]
        server = await c.request("/v1/servers", {"server": {"name": "subset", "associated_tools": [allowed["id"]]}, "team_id": connection["team"], "visibility": "team"}, admin, cf=True)
        token = await c.request("/v1/tokens", {"name": "subset", "user_email": "a@example.com", "team_id": connection["team"], "expires_in_days": 1,
                                "scope": {"server_id": server["id"], "permissions": ["servers.use", "tools.read", "tools.execute"]}}, admin, cf=True)
        async with self.connect(connection=dict(connection, server=server["id"], token=token["access_token"])) as client:
            c.require(len(await tool_pages(client)) == 1, "subset tool inventory")
            await self.data(client, "capabilities")
            before = await self.observations()
            await self.refusal(client, "search", {})
            events = (await self.observations())[len(before):]
            c.require(not any(e.get("tool") == "catalog_control_search" for e in events), "excluded tool never reaches upstream")
        async with self.connect() as client:
            await tool_pages(client)
            c.require(len(await self.pages(client)) == 2, "full server Search success")
            for org in ("b", "a"):
                updated = await c.request("/v1/gateways/" + connection["gateway"], {"auth_type": "bearer", "auth_token": c.SECRETS["tokens"][org + "-reader"]}, admin, cf=True, method="PUT", expected=200)
                c.require(updated["id"] == connection["gateway"], "credential update accepted for existing gateway")
                before = await self.observations()
                results = await self.pages(client)
                c.require({r["local"]["release"]["id"] for r in results} == {r["release"] for r in self.expected[org]}, "next call uses replacement credential")
                events = (await self.observations())[len(before):]
                c.require(any(e.get("tool") == "catalog_control_search" and e["credential"] == org + "-reader" for e in events), "credential replacement observed upstream")
        return {"sameUserSubset": True, "excludedWithoutUpstreamCall": True, "acceptedCredentialReplacement": True, "replacementData": True, "restored": True}


def exception_leaves(error):
    if isinstance(error, BaseExceptionGroup):
        return [leaf for nested in error.exceptions for leaf in exception_leaves(nested)]
    return [error]


async def execute(c, action):
    if action == "dataset":
        c.PHASE = "proxy-dataset"
        return await dataset(c)
    suite = Proxy(c)
    if action.startswith("availability-"):
        c.PHASE = action
        before = await suite.observations()
        async with suite.connect() as client:
            await tool_pages(client)
            if action == "availability-ready":
                c.require(len(await suite.pages(client)) == 2, "forwarded read after connection or recovery")
                return {"forwardedRead": True}
            translated = await suite.refusal(client, "search", {})
        events = (await suite.observations())[len(before):]
        failed_methods = sorted({e["method"] for e in events if e["path"] == "catalog" and e["status"] == 502})
        c.require(failed_methods, "actual upstream outage reached the fixed frontend")
        return {"boundedFailure": True, "translation": translated, "upstream502": True, "failedMethods": failed_methods}
    baseline = await suite.observations()
    protocols = []
    for protocol in PROTOCOLS:
        protocols.append(await suite.protocol(protocol))
        c.CASES[protocol] = protocols[-1]
    isolation = await suite.isolation_and_revocation()
    c.CASES["isolation"] = isolation
    credentials = await suite.credential_controls()
    c.CASES["credentials"] = credentials
    lifecycle = await suite.subset_and_rotation()
    events = (await suite.observations())[len(baseline):]
    c.require(not any(e["path"] in ("unexpected", "manifest") or e["path"] == "everything" and e["method"] in ("tools/call", "resources/read", "prompts/get") for e in events), "metadata reads cause no resource invocation or unexpected peer contact")
    return {"protocols": protocols, "isolation": isolation, "credentials": credentials, "lifecycle": lifecycle, "metadataOnly": True}


async def run(c, action):
    try:
        return await execute(c, action)
    except Exception:
        if action != "dataset":
            try:
                c.CASES["upstreamObservations"] = await Proxy(c).observations()
            except Exception as diagnostic:
                c.CASES["observationFailure"] = type(diagnostic).__name__
        raise
