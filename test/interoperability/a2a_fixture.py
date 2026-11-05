"""Private deployment glue: upstream routes/executor and the real SDK consumer.

Imported only by the explicit installed-tool target, never by ordinary tests.
"""

import asyncio
import copy
import json
from pathlib import Path
import ssl
import sys

from a2a_contract import CARD_PATH, URL, interaction, select_card


def deployment_card():
    """Describe this HTTPS deployment truthfully, without rewriting the upstream sample."""
    from a2a.types import AgentCapabilities, AgentCard, AgentInterface, AgentSkill
    return AgentCard(
        name="Local Hello World Agent", description="Deterministic owned A2A control", version="1.0.0",
        capabilities=AgentCapabilities(), default_input_modes=["text/plain"], default_output_modes=["text/plain"],
        supported_interfaces=[AgentInterface(url=URL, protocol_binding="JSONRPC", protocol_version="1.0")],
        skills=[AgentSkill(id="echo_bot", name="Echo Bot", description="Acknowledge the supplied request text",
                           tags=["a2a", "echo-example"], input_modes=["text/plain"], output_modes=["text/plain"])])


def serve():
    """Observe bounded requests around unchanged SDK routes; do not implement A2A here."""
    from a2a.server.request_handlers import DefaultRequestHandler
    from a2a.server.routes import create_agent_card_routes, create_jsonrpc_routes
    from a2a.server.tasks import InMemoryTaskStore
    from agent_executor import HelloWorldAgentExecutor
    from starlette.applications import Starlette
    from starlette.responses import JSONResponse
    from starlette.routing import Route
    import uvicorn

    observations = []

    async def probe(_request):
        return JSONResponse(observations)

    card = deployment_card()
    handler = DefaultRequestHandler(agent_executor=HelloWorldAgentExecutor(), task_store=InMemoryTaskStore(), agent_card=card)
    app = Starlette(routes=[Route("/observations", probe), *create_agent_card_routes(card), *create_jsonrpc_routes(handler, "/")])

    async def observed(scope, receive, send):
        if scope["type"] != "http" or scope["path"] == "/observations":
            return await app(scope, receive, send)
        assert len(observations) < 20, "unexpected request flood"
        headers = dict(scope["headers"])
        row = {"method": scope["method"], "path": scope["path"], "query": bool(scope["query_string"]),
               "authorization": b"authorization" in headers, "cookie": b"cookie" in headers,
               "version": headers.get(b"a2a-version", b"").decode(), "rpc": None, "status": None}
        body = bytearray()

        async def capture():
            event = await receive()
            if event["type"] == "http.request":
                body.extend(event.get("body", b""))
                assert len(body) <= 65536
                if not event.get("more_body") and body:
                    payload = json.loads(body)
                    row.update(rpc=payload.get("method"), jsonrpc=payload.get("jsonrpc"),
                               request_id=payload.get("id"), params=payload.get("params"))
            return event

        async def response(event):
            if event["type"] == "http.response.start":
                row["status"] = event["status"]
            await send(event)

        observations.append(row)
        await app(scope, capture, response)

    uvicorn.run(observed, host="127.0.0.1", port=9443, ssl_keyfile="/server/key.pem",
                ssl_certfile="/server/cert.pem", access_log=False, log_level="warning")


async def client(mode="direct"):
    """Resolve once, then give the retained protobuf Card to the actual SDK factory."""
    import httpx
    from google.protobuf.json_format import MessageToDict, ParseDict
    from a2a.client import ClientConfig, create_client
    from a2a.types import AgentCard, GetTaskRequest, Message, Part, Role, SendMessageConfiguration, SendMessageRequest

    trust = ssl.create_default_context() if mode == "untrusted-ca" else ssl.create_default_context(cafile="/client/ca.pem")
    governed = mode != "direct"
    message, message_id, context_id = interaction(governed)
    async with httpx.AsyncClient(verify=trust, trust_env=False, follow_redirects=False,
                                 timeout=5, headers={"A2A-Version": "1.0"}) as http:
        if governed:
            raw = Path("/client/card.json").read_bytes()
            assert len(raw) < 65536
            card = json.loads(raw)
        else:
            response = await http.get(URL.rstrip("/") + CARD_PATH)
            response.raise_for_status()
            assert len(response.content) < 65536
            card = response.json()
        result = {"card": card, "card_sha256": select_card(card), "selection_refusals": []}
        for case in ("version", "binding", "endpoint", "ambiguous"):
            invalid = copy.deepcopy(card)
            interface = invalid["supportedInterfaces"][0]
            if case == "ambiguous":
                invalid["supportedInterfaces"].append(copy.deepcopy(interface))
            else:
                key, value = {"version": ("protocolVersion", "0.3"), "binding": ("protocolBinding", "HTTP+JSON"),
                              "endpoint": ("url", "https://external.invalid/")}[case]
                interface[key] = value
            try:
                select_card(invalid)
            except ValueError:
                result["selection_refusals"].append(case)
            else:
                raise AssertionError("unsupported selection accepted")
        sdk = await create_client(ParseDict(card, AgentCard()), client_config=ClientConfig(
            httpx_client=http, supported_protocol_bindings=["JSONRPC"], streaming=False, polling=False,
            accepted_output_modes=["text/plain"]))
        async with sdk:
            request = SendMessageRequest(message=Message(message_id=message_id, context_id=context_id,
                role=Role.ROLE_USER, parts=[Part(text=message, media_type="text/plain")]),
                configuration=SendMessageConfiguration(history_length=2))
            responses = [MessageToDict(event) async for event in sdk.send_message(request)]
            assert len(responses) == 1 and set(responses[0]) == {"task"}, "expected one native non-streaming task"
            result["send"] = responses[0]["task"]
            result["readback"] = MessageToDict(await sdk.get_task(GetTaskRequest(id=result["send"]["id"], history_length=1)))
        print(json.dumps(result, ensure_ascii=False))


def catalog_request():
    """Private fixed-origin test control; no proxies, redirects or metadata destinations."""
    import http.client
    request = json.loads(Path("/catalog/request.json").read_text())
    path = request["path"]
    assert path.startswith("/api/v1/") or path in ("/.well-known/ard.json", "/livez", "/readyz")
    assert "\\" not in path and "#" not in path and request["method"] in ("GET", "POST")
    headers = {"Accept": "application/json"}
    for field, header in (("token", "Authorization"), ("key", "Idempotency-Key"), ("etag", "If-Match")):
        if request[field]:
            headers[header] = ("Bearer " if field == "token" else "") + request[field]
    body = None if request["body"] is None else json.dumps(request["body"]).encode()
    if body is not None:
        headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection("127.0.0.1", 8080, timeout=10)
    try:
        connection.request(request["method"], path, body=body, headers=headers)
        response = connection.getresponse()
        raw = response.read(2*1024*1024 + 1)
        assert len(raw) <= 2*1024*1024
        value = json.loads(raw)
        print(json.dumps({"status": response.status, "etag": response.getheader("ETag", ""), "body": value}))
    finally:
        connection.close()


def consume(mode):
    """Keep native exception types/causes; only the supervisor decides expected failure."""
    try:
        asyncio.run(asyncio.wait_for(client(mode), timeout=20))
    except Exception as error:
        causes = []
        cause = error
        seen = set()
        while cause is not None and id(cause) not in seen:
            seen.add(id(cause))
            causes.append({"type": type(cause).__name__, "message": str(cause)[:2000]})
            cause = cause.__cause__ or cause.__context__
        print(json.dumps({"error": type(error).__name__, "causes": causes}))
        sys.exit(3)


def probe():
    import urllib.request
    # This process mounts only its own certificate/key; it never leaves loopback.
    context = ssl.create_default_context(cafile="/server/ca.pem")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    with opener.open(URL + "observations", timeout=2) as response:
        raw = response.read(65537)
    assert len(raw) <= 65536
    print(raw.decode())


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("A2A fixture requires Python assertions")
    if sys.argv[1:] == ["serve"]:
        serve()
    elif len(sys.argv) == 3 and sys.argv[1] == "client" and sys.argv[2] in ("direct", "governed", "untrusted-ca", "unavailable"):
        consume(sys.argv[2])
    elif sys.argv[1:] == ["catalog-request"]:
        catalog_request()
    elif sys.argv[1:] == ["probe"]:
        probe()
    else:
        raise ValueError("unknown A2A fixture action")
