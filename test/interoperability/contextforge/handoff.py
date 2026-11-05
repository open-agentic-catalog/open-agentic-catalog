"""Governed selection and explicit native use of one owned virtual server."""

import copy
import hashlib
import json
from pathlib import Path
import uuid

if __package__:
    from .handoff_checks import CARD, IDENTIFIER, TAG, VERSION, RESOURCE_SHA256, document, map_client, validate_record
    from .publication import publish
    from .proxy import exception_leaves
else:
    from handoff_checks import CARD, IDENTIFIER, TAG, VERSION, RESOURCE_SHA256, document, map_client, validate_record
    from publication import publish
    from proxy import exception_leaves


def saved(name):
    return json.loads((Path("/state") / name).read_text())


class Handoff:
    def __init__(self, c):
        self.c = c
        self.private = saved("contextforge-connections.json")
        self.connection = self.private["connections"]["everything"]

    async def observations(self):
        result = await self.c.request("/observations")
        self.c.require(result["overflow"] == 0, "complete handoff observations")
        return result["events"]

    async def publish(self):
        c = self.c
        c.PHASE = "handoff-publication"
        baseline = len(await self.observations())
        publisher = saved("dataset.json")["a"][0]["publisher"]
        expected = await publish(c, "a", kind="mcp_server", identifier=IDENTIFIER, version=VERSION,
                                 representations=[{"role": "primary", "mediaType": CARD, "data": document(self.connection["server"])}],
                                 name="ContextForge Everything virtual server", tags=[TAG], publisher=publisher, description="Owned gateway for explicit MCP client checks.")
        expected.update(server=self.connection["server"], metadataBaseline=baseline)
        record = await c.request("/api/v1/catalog/resources/" + expected["release"], token=c.SECRETS["tokens"]["a-reader"])
        representation = validate_record(record, expected)
        expected.update(representation=representation["id"], cardDigest=representation["contentDigest"], evidence=record["evidence"]["id"])
        c.save("handoff-expected.json", expected)
        return expected

    async def draft(self):
        c = self.c
        c.PHASE = "handoff-later-draft"
        expected = saved("handoff-expected.json")
        card = document(expected["server"])
        card.update(version="1.0.1", description="Unpublished descriptor. Its endpoint must remain unused.")
        card["remotes"][0]["url"] += "-unpublished"
        token = c.SECRETS["tokens"]["a-contributor"]
        created = await c.request("/api/v1/resources/" + expected["resource"] + "/versions", {"version": "1.0.1", "representations": [{"role": "primary", "mediaType": CARD, "data": card}]}, token)
        listing, etag = await c.request("/api/v1/catalog-listings/" + expected["listing"], token=token, return_headers=True)
        body = copy.deepcopy(expected["listingBody"])
        body.update(versionRevisionId=created["revision"]["id"], description="Unpublished later listing draft.")
        revision = await c.request("/api/v1/catalog-listings/" + listing["id"] + "/revisions", body, token, etag=etag)
        c.require(created["revision"]["id"] != expected["versionRevision"] and revision["revision"]["id"] != expected["listingRevision"], "later draft has distinct revisions")
        return {"versionRevision": created["revision"]["id"], "listingRevision": revision["revision"]["id"]}

    async def map(self):
        c = self.c
        c.PHASE = "handoff-map"
        expected = saved("handoff-expected.json")
        for label, token, status in (("missing", None, 401), ("invalid", c.SECRETS["tokens"]["a-audience"], 401), ("wrong-tenant", c.SECRETS["tokens"]["b-reader"], 404)):
            problem = await c.request("/api/v1/catalog/resources/" + expected["release"], token=token, expected=status)
            c.require(problem["status"] == status and "data" not in problem, "denied Catalog read: " + label)
        events = (await self.observations())[expected["metadataBaseline"]:]
        c.require(not any(e["path"] == "everything" and e["method"] in ("tools/call", "resources/read", "prompts/get") for e in events), "metadata selection causes no runtime invocation")
        c.require(not Path("/state/handoff-client.json").exists(), "denied reads create no client configuration")
        selected = saved("handoff-selected.json")
        config = map_client(selected, expected, self.connection["token"])
        c.save("handoff-client.json", config)
        return {"url": config["url"], "server": expected["server"], "metadataOnly": True, "deniedReads": ["missing", "invalid", "wrong-tenant"], "separateCredential": True}

    def connect(self, *, headers=None, responses=None):
        config = saved("handoff-client.json")
        return self.c.mcp(config["url"], config["headers"] if headers is None else headers, responses=responses)

    async def echo(self, action):
        c = self.c
        names = saved("handoff-names.json")
        before = len(await self.observations())
        marker = "ContextForge " + action + " " + uuid.uuid4().hex
        async with self.connect() as client:
            value = await client.call_tool(names["echo"], {"message": marker}, read_timeout_seconds=12)
            c.require(not value.is_error and value.content[0].text == "Echo: " + marker, "exact native echo result")
            info = client.server_info.model_dump(by_alias=True, exclude_none=True)
            c.require(client.protocol_version == "2026-07-28", "modern virtual endpoint")
        events = (await self.observations())[before:]
        c.require(any(e["path"] == "everything" and e.get("tool") == "echo" and e["status"] == 200 for e in events), "echo forwards to the real upstream")
        c.require(all(e["credential"] == "none" and e.get("testKey") for e in events if e["path"] == "everything" and e["status"] == 200), "runtime credentials stay separate")
        return {"echo": True, "serverInfo": info, "forwarded": True}

    async def runtime(self):
        c = self.c
        c.PHASE = "handoff-runtime"
        baseline = len(await self.observations())
        async with self.connect() as discovery:
            tools = (await discovery.list_tools()).tools
            resources = (await discovery.list_resources()).resources
            prompts = (await discovery.list_prompts()).prompts
            c.require(len(tools) == len(resources) == len(prompts) == 1, "selected native tool, resource and prompt")
            c.require(tools[0].name == self.connection["selected"]["tools"][0]["name"], "native imported echo name")
            names = {"echo": tools[0].name, "resource": str(resources[0].uri), "prompt": prompts[0].name}
            info = discovery.server_info.model_dump(by_alias=True, exclude_none=True)
            c.require(discovery.protocol_version == "2026-07-28", "observed virtual endpoint protocol")
        c.save("handoff-names.json", names)
        c.PHASE = "handoff-prompt-first"
        prompt_start = len(await self.observations())
        try:
            async with self.connect() as fresh:
                result = await fresh.get_prompt(names["prompt"])
                c.require(len(result.messages) == 1 and result.messages[0].role == "user" and result.messages[0].content.text == "This is a simple prompt without arguments.", "exact native prompt messages")
            prompt = {"status": "passed"}
        except Exception as error:
            leaves = exception_leaves(error)
            c.require(all(type(e).__name__ in ("MCPError", "McpError", "HTTPStatusError") for e in leaves), "classified native prompt refusal")
            prompt_events = (await self.observations())[prompt_start:]
            cause = "upstream_unauthorized" if any(e["path"] == "everything" and e["status"] == 401 for e in prompt_events) else "unclassified"
            prompt = {"status": "peer_limited", "cause": cause, "errorTypes": sorted({type(e).__name__ for e in leaves})}
        c.PHASE = "handoff-echo-resource"
        await self.echo("governed-handoff")
        async with self.connect() as client:
            resource = await client.read_resource(names["resource"])
            c.require(len(resource.contents) == 1, "one selected static resource")
            text = resource.contents[0].text
            sha = hashlib.sha256(text.encode()).hexdigest()
            c.require(sha == RESOURCE_SHA256, "resource bytes match the direct native control")
        events = (await self.observations())[baseline:]
        everything = [e for e in events if e["path"] == "everything"]
        c.require(not any(e["path"] == "unexpected" for e in events), "no unexpected metadata destination")
        c.require(all(e["credential"] == "none" for e in everything), "Catalog and ContextForge bearer tokens never reach Everything")
        protocols = sorted({e["protocol"] for e in everything if e["method"] in ("tools/call", "resources/read", "prompts/get") and e["status"] == 200 and e.get("protocol")})
        c.save("handoff-runtime-info.json", info)
        return {"echo": True, "resourceSha256": sha, "protocol": "2026-07-28", "upstreamProtocols": protocols, "nativeNames": names,
                "serverInfo": info, "prompt": prompt, "promptFirst": True, "credentialsSeparated": True}

    async def refusals(self):
        c = self.c
        c.PHASE = "handoff-runtime-refusals"
        names = saved("handoff-names.json")
        baseline = len(await self.observations())
        for headers, status in (({}, 401), ({"Authorization": "Bearer " + self.private["connections"]["a"]["token"]}, 403)):
            responses = []
            try:
                async with self.connect(headers=headers, responses=responses) as client:
                    await client.call_tool(names["echo"], {"message": "refused runtime caller"}, read_timeout_seconds=12)
            except Exception as error:
                c.require(all(type(e).__name__ in ("MCPError", "McpError", "HTTPStatusError") for e in exception_leaves(error)), "classified native runtime refusal")
                c.require(status in responses and all(code < 400 or code == status for code in responses), "actual native runtime admission refusal")
            else:
                raise AssertionError("runtime credential refusal unexpectedly succeeded")
        events = (await self.observations())[baseline:]
        c.require(not any(e["path"] == "everything" and e["method"] == "tools/call" for e in events), "denied runtime caller causes no upstream invocation")
        return {"missing": 401, "wrongTeam": 403, "noInvocation": True}

    async def down(self, service):
        c = self.c
        c.PHASE = "handoff-down-" + service
        c.require(service in ("everything", "contextforge"), "owned runtime outage")
        names = saved("handoff-names.json")
        before = len(await self.observations())
        responses, result, failure = [], None, None
        try:
            async with self.connect(responses=responses) as client:
                result = await client.call_tool(names["echo"], {"message": "outage-" + service + "-" + uuid.uuid4().hex}, read_timeout_seconds=12)
        except Exception as error:
            failure = error
        c.require(result is None or result.is_error is True, "stopped runtime cannot return a successful tool result")
        c.require(all(200 <= code < 300 or code >= 500 for code in responses), "authentication, permission and rate-limit refusals cannot qualify an outage")
        if service == "contextforge":
            leaves = exception_leaves(failure) if failure is not None else []
            c.require(bool(leaves) and all(type(e).__name__ in ("ConnectError", "MCPError", "McpError") for e in leaves)
                      and not any("CERTIFICATE_VERIFY_FAILED" in str(e) for e in leaves), "owned gateway connection failure")
            c.require(not responses, "stopped gateway cannot return an HTTP authentication or application response")
        elif failure is not None:
            c.require(all(type(e).__name__ in ("MCPError", "McpError") for e in exception_leaves(failure)), "bounded upstream tool refusal")
        else:
            c.require(result is not None and result.is_error is True, "actual upstream tool error result")
        events = (await self.observations())[before:]
        if service == "everything":
            c.require(any(e["path"] == "everything" and e.get("method") in ("initialize", "tools/call") and e["status"] == 502 for e in events), "forwarded MCP request reaches the stopped upstream")
        else:
            c.require(not any(e["path"] == "everything" for e in events), "stopped gateway cannot invoke Everything")
        return {"boundedFailure": True, "service": service, "upstream502": service == "everything"}


async def run(c, action):
    suite = Handoff(c)
    if action == "handoff-publish":
        return await suite.publish()
    if action == "handoff-draft":
        return await suite.draft()
    if action == "handoff-map":
        return await suite.map()
    if action == "handoff-runtime":
        return await suite.runtime()
    if action == "handoff-refusals":
        return await suite.refusals()
    if action in ("handoff-down-everything", "handoff-down-contextforge"):
        return await suite.down(action.removeprefix("handoff-down-"))
    if action == "handoff-ready":
        c.PHASE = action
        server = await c.request("/v1/servers/" + suite.connection["server"], token=suite.private["admin"], cf=True)
        c.require(server["id"] == suite.connection["server"], "same persisted virtual-server ID")
        result = await suite.echo("recovery")
        c.require(result["serverInfo"] == saved("handoff-runtime-info.json"), "same native application after recovery")
        return dict(result, sameServer=True)
    raise ValueError("unknown handoff action")
