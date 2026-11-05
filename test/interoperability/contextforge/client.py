"""Native controls through the installed independent Python MCP SDK."""

import asyncio
from contextlib import asynccontextmanager
import hashlib
import json
import logging
import re
from pathlib import Path
import ssl
import sys
import time
import uuid

import httpx2
from mcp import Client
from mcp.client.streamable_http import streamable_http_client
from checks import proxy_progress, token_times, tool_pages

ORIGIN = "https://federation-fixture:8443"
CF = "https://contextforge:4444"
CA = "/private/frontend/ca.pem"
SECRETS = json.loads(Path("/private/secrets.json").read_text())
TOOLS = {"catalog_control_" + x for x in ("capabilities", "search", "release", "indexed_record", "explore", "access")}
PHASE = "setup"
CASES = {}
HTTP_FAILURE = None
PERMISSIONS = ["servers.use", "tools.read", "tools.execute", "resources.read", "prompts.read"]


def require(condition, message):
    if not condition:
        raise AssertionError(message)


async def request(path, body=None, token=None, cf=False, expected=None, etag=None, method=None, return_headers=False):
    global HTTP_FAILURE
    origin = CF if cf else ORIGIN
    headers = {"Authorization": "Bearer " + token} if token else {}
    if body is not None and not cf:
        headers["Idempotency-Key"] = str(uuid.uuid4())
    if etag:
        headers["If-Match"] = etag
    wall, monotonic = time.time(), time.monotonic()
    async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=30, follow_redirects=False) as client:
        response = await client.request(method or ("POST" if body is not None else "GET"), origin + path, json=body, headers=headers)
    if response.status_code != (expected or (201 if body is not None else 200)):
        HTTP_FAILURE = {"path": path.split("?")[0], "status": response.status_code, "wallBefore": wall, "wallAfter": time.time(),
                        "elapsed": time.monotonic() - monotonic}
        if token:
            # Inspect only our synthetic token's timestamps, never its identity or raw bytes.
            HTTP_FAILURE["tokenTimes"] = token_times(token)
            if cf and response.status_code == 401 and token_times(token):
                target = Path("/state/native-failure-token")
                target.write_text(token)
                target.chmod(0o600)
    require(response.status_code == (expected or (201 if body is not None else 200)),
            "native REST status " + str(response.status_code) + " at " + path.split("?")[0])
    require(len(response.content) < 2 * 1024 * 1024, "REST response bound")
    value = response.json() if response.content else None
    return (value, response.headers.get("etag")) if return_headers else value


def save(name, value):
    path = Path("/state") / name
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


async def frontend():
    for _ in range(50):
        try:
            discovery = await request("/.well-known/openid-configuration")
            require(discovery["issuer"] == ORIGIN and discovery["jwks_uri"] == ORIGIN + "/keys", "fixed HTTPS issuer")
            require(len((await request("/keys"))["keys"]) == 1, "public signing key")
            return {"privateTLSIssuer": True}
        except (httpx2.HTTPError, AssertionError):
            await asyncio.sleep(0.2)
    raise AssertionError("owned issuer did not become ready")


async def identities():
    results = {}
    for org in ("a", "b"):
        admin = SECRETS["tokens"][org + "-admin"]
        namespace = await request("/api/v1/namespaces", {"slug": "contextforge-" + org, "displayName": "ContextForge " + org}, admin)
        grant = await request("/api/v1/role-assignments", {"subject": org + "-reader", "actorType": "human", "role": "reader",
                              "namespaceId": namespace["id"], "reason": "Owned ContextForge native read control"}, admin)
        reader = await request("/api/v1/me", token=SECRETS["tokens"][org + "-reader"])
        discovery = await request("/api/v1/access/roles?key=discovery_reader&state=active&limit=1", token=admin)
        require(len(discovery["items"]) == 1 and discovery["items"][0]["key"] == "discovery_reader", "exact discovery template")
        await request("/api/v1/access/assignments", {"roleDefinitionId": discovery["items"][0]["id"], "membershipId": reader["membershipId"],
                      "reason": "Owned ContextForge metadata discovery"}, admin)
        for role in ("contributor", "reviewer", "publisher"):
            await request("/api/v1/role-assignments", {"subject": org + "-" + role, "actorType": "human", "role": role,
                          "namespaceId": namespace["id"], "reason": "Owned ContextForge publication identity"}, admin)
            authority = await request("/api/v1/me", token=SECRETS["tokens"][org + "-" + role])
            require("access.manage" not in authority["availablePermissions"] and not authority["permissions"], "publication identity has namespace-only authority")
        access = await request("/api/v1/me", token=SECRETS["tokens"][org + "-reader"])
        require("access.manage" not in access["availablePermissions"] and "resource.read" not in access["permissions"], "reader has no tenant administrator or tenant-wide read")
        require(any(g["namespaceId"] == namespace["id"] for g in access["namespaceGrants"]), "exact namespace grant")
        results[org] = {"namespace": namespace["id"], "grant": grant, "membership": access["membershipId"]}
    save("catalog-identities.json", results)
    return {"twoSharedTenants": True, "namespaceReaders": True, "scopedPublicationIdentities": True}


@asynccontextmanager
async def mcp(url, headers, mode="auto", private_ca=True, responses=None):
    async def observe(response):
        global HTTP_FAILURE
        progress = proxy_progress(PHASE, response.request.method, response.request.content, response.status_code)
        if progress and not Path("/state/proxy-progress.json").exists():
            save("proxy-progress.json", progress)
        if responses is not None:
            responses.append(response.status_code)
        if response.status_code >= 400:
            HTTP_FAILURE = {"phase": PHASE, "path": response.request.url.path, "status": response.status_code}

    trust = ssl.create_default_context(cafile=CA) if private_ca else ssl.create_default_context()
    async with httpx2.AsyncClient(verify=trust, trust_env=False, timeout=30, headers=headers, event_hooks={"response": [observe]}) as http:
        async with Client(streamable_http_client(url, http_client=http), mode=mode, cache=None) as client:
            yield client


def envelope(value):
    result = value.model_dump(by_alias=True, exclude_none=True)
    require(not result.get("isError"), "unexpected native Catalog tool refusal")
    require(json.loads(result["content"][0]["text"]) == result["structuredContent"], "matching native envelopes")
    return result["structuredContent"]["data"]


async def catalog_native():
    bearer = SECRETS["tokens"]["a-reader"]
    async with mcp(ORIGIN + "/mcp", {"Authorization": "Bearer " + bearer}) as client:
        listed = await client.list_tools()
        names = [tool.name for tool in listed.tools]
        require(len(names) == 6 and set(names) == TOOLS, "six unique native Catalog tools")
        for tool in listed.tools:
            value = tool.model_dump(by_alias=True, exclude_none=True)
            require(value["annotations"]["readOnlyHint"] and value["inputSchema"]["additionalProperties"] is False and value["outputSchema"], "read-only closed tool schemas")
        kinds = envelope(await client.call_tool("catalog_control_capabilities", {}, read_timeout_seconds=10))["resourceKinds"]
        require(set(kinds) == {"agent", "mcp_server", "skill", "plugin"}, "native resource kinds")
        access = envelope(await client.call_tool("catalog_control_access", {}, read_timeout_seconds=10))
        direct = await request("/api/v1/me", token=bearer)
        require(access["tenantId"] == direct["tenantId"] and access["permissions"] == direct["permissions"], "MCP uses current API authority")
        return {"tools": sorted(names), "protocol": client.protocol_version, "capabilitiesAndAccess": True}


async def everything_native():
    async with mcp(ORIGIN + "/everything/mcp", {"X-MCP-Test-Key": SECRETS["everythingKey"]}, mode="legacy") as client:
        require(client.server_info.name == "mcp-servers/everything", "native Everything identity")
        require("echo" in {t.name for t in (await client.list_tools()).tools}, "native echo discovery")
        echo = await client.call_tool("echo", {"message": "ContextForge native control"}, read_timeout_seconds=10)
        require(not echo.is_error and echo.content[0].text == "Echo: ContextForge native control", "native echo bytes")
        uri = "demo://resource/static/document/features.md"
        require(uri in {str(r.uri) for r in (await client.list_resources()).resources}, "native static resource discovery")
        resource = await client.read_resource(uri)
        text = resource.contents[0].text
        require(text.startswith("# Everything Server - Features") and str(resource.contents[0].uri) == uri, "native static resource bytes and URI")
        prompt = await client.get_prompt("simple-prompt")
        require(len(prompt.messages) == 1 and prompt.messages[0].role == "user" and prompt.messages[0].content.text == "This is a simple prompt without arguments.", "native prompt bytes")
        return {"protocol": client.protocol_version, "version": client.server_info.version, "echo": True, "resource": True, "prompt": True,
                "resourceSha256": hashlib.sha256(text.encode()).hexdigest()}


def rows(value):
    require(isinstance(value, list), "native flat inventory")
    return value


async def contextforge_native():
    global PHASE
    login = await request("/v1/auth/email/login", {"email": "admin@example.com", "password": SECRETS["cfPassword"]}, cf=True, expected=200)
    admin = login["access_token"]
    version = await request("/v1/version", token=admin, cf=True)
    require(version["app"]["version"] == json.loads(Path("/code/inputs.json").read_text())["version"], "running ContextForge version")
    async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=15) as http:
        wall, monotonic = time.time(), time.monotonic()
        page = await http.get(CF + "/v1/admin/", headers={"Authorization": "Bearer " + admin})
        if page.status_code != 200:
            global HTTP_FAILURE
            HTTP_FAILURE = {"path": "/v1/admin/", "status": page.status_code, "wallBefore": wall, "wallAfter": time.time(), "elapsed": time.monotonic() - monotonic,
                            "tokenTimes": token_times(admin)}
            target = Path("/state/native-failure-token")
            target.write_text(admin)
            target.chmod(0o600)
        require(page.status_code == 200, "native Admin UI status " + str(page.status_code))
        require("text/html" in page.headers.get("content-type", "") and b"ContextForge" in page.content, "authenticated native Admin UI content")
    setup, counts = {}, {}
    for label in ("a", "b", "everything"):
        permissions = PERMISSIONS if label == "everything" else PERMISSIONS[:3]
        runtime_role = await request("/v1/rbac/roles", {"name": "native_" + label, "scope": "team", "permissions": permissions}, admin, cf=True, expected=200)
        team = await request("/v1/teams/", {"name": "Catalog control " + label, "visibility": "private"}, admin, cf=True)
        email = label + "@example.com"
        password_key = {"a": "cfPasswordA", "b": "cfPasswordB", "everything": "cfPasswordEverything"}[label]
        await request("/v1/auth/email/admin/users", {"email": email, "password": SECRETS[password_key], "full_name": "Native " + label,
                      "is_admin": False, "is_active": True, "password_change_required": False}, admin, cf=True)
        await request("/v1/teams/" + team["id"] + "/members", {"email": email, "role": "member"}, admin, cf=True)
        await request("/v1/rbac/users/" + email + "/roles", {"role_id": runtime_role["id"], "scope": "team", "scope_id": team["id"]}, admin, cf=True, expected=200)
        gateway_body = {"name": "native-" + label, "url": ORIGIN + ("/everything/mcp" if label == "everything" else "/mcp"),
                        "transport": "STREAMABLEHTTP", "ca_certificate": Path(CA).read_text(), "team_id": team["id"], "visibility": "team",
                        "gateway_mode": "cache", "passthrough_headers": []}
        if label == "everything":
            gateway_body.update(auth_type="authheaders", auth_headers=[{"key": "X-MCP-Test-Key", "value": SECRETS["everythingKey"]}])
        else:
            gateway_body.update(auth_type="bearer", auth_token=SECRETS["tokens"][label + "-reader"])
        gateway = await request("/v1/gateways", gateway_body, admin, cf=True, expected=200)
        inventory = {}
        for kind in ("tools", "resources", "prompts"):
            inventory[kind] = rows(await request("/v1/" + kind + "?gateway_id=" + gateway["id"] + "&limit=50", token=admin, cf=True))
        if label != "everything":
            require(len(inventory["tools"]) == 6 and not inventory["resources"] and not inventory["prompts"], "tool-only Catalog registration")
        else:
            require(inventory["tools"] and inventory["resources"] and inventory["prompts"], "Everything registration includes all three capability types")
        selected = {"tools": inventory["tools"] if label != "everything" else [t for t in inventory["tools"] if t.get("originalName", t.get("original_name")) == "echo"],
                    "resources": [r for r in inventory["resources"] if r["uri"] == "demo://resource/static/document/features.md"],
                    "prompts": [p for p in inventory["prompts"] if p.get("originalName", p.get("original_name", p.get("name"))) == "simple-prompt"]}
        require(len(selected["tools"]) == (1 if label == "everything" else 6), "selected native tool inventory")
        require(len(selected["resources"]) == len(selected["prompts"]) == (1 if label == "everything" else 0), "selected native resource and prompt inventory")
        for values in selected.values():
            for item in values:
                uuid.UUID(item["id"])
        server = await request("/v1/servers", {"server": {"name": "native-" + label,
                               **{"associated_" + kind: [x["id"] for x in values] for kind, values in selected.items()}},
                               "team_id": team["id"], "visibility": "team"}, admin, cf=True)
        token = await request("/v1/tokens", {"name": "native-" + label, "user_email": email, "team_id": team["id"], "expires_in_days": 1,
                              "scope": {"server_id": server["id"], "permissions": permissions}}, admin, cf=True)
        PHASE = "contextforge-" + label + "-runtime"
        async with mcp(CF + "/servers/" + server["id"] + "/mcp", {"Authorization": "Bearer " + token["access_token"]}) as client:
            listed = await tool_pages(client)
            require({t.name for t in listed} == {t["name"] for t in selected["tools"]}, "server-scoped native token can list only the selected tools")
            if label != "everything":
                capability = next(t["name"] for t in selected["tools"] if t.get("originalName", t.get("original_name")) == "catalog_control_capabilities")
                require(set(envelope(await client.call_tool(capability, {}, read_timeout_seconds=10))["resourceKinds"]) == {"agent", "mcp_server", "skill", "plugin"}, "native scoped token can execute a read-only Catalog tool")
        await request("/v1/servers", {"server": {"name": "refused-runtime-write"}, "team_id": team["id"], "visibility": "team"}, token["access_token"], cf=True, expected=403)
        setup[label] = {"team": team["id"], "gateway": gateway["id"], "server": server["id"], "token": token["access_token"], "selected": selected}
        counts[label] = {kind: len(values) for kind, values in selected.items()}
    # Credentials stay in the private, invocation-owned mount, never in reports.
    save("contextforge-connections.json", {"admin": admin, "connections": setup})
    return {"version": version["app"]["version"], "adminUI": True, "nativeRegistration": counts, "nativeTeamRolesAndServerScopedTokens": True,
            "scopedRuntimeReadAndWriteDenial": True}


async def refusals():
    global HTTP_FAILURE
    # These checks follow healthy controls. Transport failures are not denials.
    await request("/api/v1/me", expected=401)
    async with httpx2.AsyncClient(verify=ssl.create_default_context(cafile=CA), trust_env=False, timeout=5) as client:
        denied = await client.post(ORIGIN + "/everything/mcp", json={"jsonrpc": "2.0", "method": "initialize", "id": 1})
        require(denied.status_code == 401, "Everything requires its own key")
        await request("/v1/rbac/my/permissions", cf=True, expected=401)
    try:
        await request("/v1/rbac/my/permissions", token="invalid-native-token", cf=True, expected=200)
    except AssertionError as error:
        require(str(error) == "native REST status 401 at /v1/rbac/my/permissions", "preserve the HTTP refusal during diagnostics")
        require(HTTP_FAILURE["status"] == 401 and HTTP_FAILURE["tokenTimes"] == {}, "opaque token diagnostics remain empty")
        HTTP_FAILURE = None
    else:
        raise AssertionError("invalid ContextForge token was accepted")
    try:
        async with httpx2.AsyncClient(trust_env=False, timeout=5) as client:
            await client.get(ORIGIN + "/healthz")
    except httpx2.ConnectError as error:
        require("CERTIFICATE_VERIFY_FAILED" in str(error), "untrusted CA fails certificate verification")
    else:
        raise AssertionError("private CA unexpectedly trusted")
    return {"missingCatalogToken": True, "missingEverythingKey": True, "missingContextForgeToken": True, "untrustedCA": True,
            "invalidContextForgeToken": True, "httpFailureDiagnostic": True}


async def main():
    global PHASE
    action = sys.argv[1]
    if action.startswith("handoff-"):
        import handoff
        return await handoff.run(sys.modules[__name__], action)
    if action in ("dataset", "proxy", "availability-ready", "availability-down"):
        import proxy
        return await proxy.run(sys.modules[__name__], action)
    if action in ("frontend", "identities"):
        return await {"frontend": frontend, "identities": identities}[action]()
    require(action == "native", "select a native control action")
    for name, method in (("catalog", catalog_native), ("everything", everything_native), ("contextforge", contextforge_native), ("refusals", refusals)):
        PHASE = name
        CASES[name] = await method()
    return CASES


def safe_errors(error):
    if isinstance(error, BaseExceptionGroup):
        return [item for nested in error.exceptions for item in safe_errors(nested)]
    value = {"type": type(error).__name__}
    if isinstance(error, AssertionError):
        value["assertion"] = str(error)
    if isinstance(error, httpx2.HTTPStatusError):
        value["status"] = error.response.status_code
    if type(error).__name__ in ("MCPError", "McpError"):
        value["code"] = error.code
        message = str(error) + " " + json.dumps(error.data)
        credentials = list(SECRETS["tokens"].values())
        path = Path("/state/contextforge-connections.json")
        if path.exists():
            saved = json.loads(path.read_text())
            credentials += [saved["admin"]] + [item["token"] for item in saved["connections"].values()]
        for credential in credentials:
            message = message.replace(credential, "[redacted]")
        value["message"] = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[redacted token]", message)[:512]
    return [value]


if __name__ == "__main__":
    # Library error logs can include HTTP bodies. Return only our phase and type.
    logging.disable(logging.CRITICAL)
    started = time.monotonic()
    try:
        print(json.dumps(asyncio.run(main())))
    except BaseException as error:
        save("client-failure.json", {"phase": PHASE, "type": type(error).__name__, "passed": CASES, "elapsed": round(time.monotonic() - started, 2),
                                   "errors": safe_errors(error), "httpFailure": HTTP_FAILURE})
        print("Native control failed during " + PHASE + ": " + type(error).__name__, file=sys.stderr)
        raise SystemExit(1) from None
