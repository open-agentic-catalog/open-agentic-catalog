"""Persistent local deployment of the locked unchanged Hello World executor.

No finite test observer, model, Catalog token or unrestricted diagnostic output.
"""
import asyncio
import json
import logging
from pathlib import Path


async def reply(send, status):
    await send({"type": "http.response.start", "status": status, "headers": []})
    await send({"type": "http.response.body", "body": b"Local demo request refused", "more_body": False})


def guard(app):
    slots = asyncio.Semaphore(8)
    admitted = 0

    async def guarded(scope, receive, send):
        nonlocal admitted
        if scope['type'] != 'http':
            return await app(scope, receive, send)
        headers = dict(scope['headers'])
        if b'authorization' in headers or b'cookie' in headers:
            return await reply(send, 401)
        if scope['query_string'] or scope['path'] not in ('/', '/.well-known/agent-card.json'):
            return await reply(send, 404)
        if (scope['path'] == '/' and scope['method'] != 'POST') or (scope['path'] != '/' and scope['method'] != 'GET'):
            return await reply(send, 405)
        if scope['method'] == 'POST':
            data = bytearray()
            while True:
                event = await receive()
                if event['type'] != 'http.request':
                    return
                data.extend(event.get('body', b''))
                if len(data) > 65536:
                    return await reply(send, 413)
                if not event.get('more_body'):
                    break
            try:
                payload = json.loads(data)
            except (ValueError, UnicodeError):
                return await reply(send, 400)
            if not isinstance(payload, dict) or payload.get('method') not in ('SendMessage', 'GetTask'):
                return await reply(send, 403)
            if payload['method'] == 'SendMessage':
                if admitted >= 256:
                    return await reply(send, 429)
                admitted += 1
            delivered = False

            async def frozen():
                nonlocal delivered
                if not delivered:
                    delivered = True
                    return {'type': 'http.request', 'body': bytes(data), 'more_body': False}
                return await receive()
            actual_receive = frozen
        else:
            actual_receive = receive
        started = False
        async def safe_send(event):
            nonlocal started
            if event['type'] == 'http.response.start':
                started = True
            await send(event)
        try:
            async with slots:
                await asyncio.wait_for(app(scope, actual_receive, safe_send), timeout=15)
        except Exception:
            # Dependency exceptions/payloads are private; no traceback or value is logged.
            if not started:
                await reply(send, 500)
            return

    return guarded


def serve():
    # All dependency diagnostics are private in this owned demo. Readiness/client checks
    # provide the useful failure boundary; never emit SDK exception chains or payloads.
    logging.disable(logging.CRITICAL)
    from a2a.server.request_handlers import DefaultRequestHandler
    from a2a.server.routes import create_agent_card_routes, create_jsonrpc_routes
    from a2a.server.tasks import InMemoryTaskStore
    from a2a.types import AgentCard
    from agent_executor import HelloWorldAgentExecutor
    from google.protobuf.json_format import ParseDict
    from starlette.applications import Starlette
    import uvicorn

    card = ParseDict(json.loads(Path('/private/card.json').read_text()), AgentCard())
    handler = DefaultRequestHandler(agent_executor=HelloWorldAgentExecutor(), task_store=InMemoryTaskStore(), agent_card=card)
    app = Starlette(routes=[*create_agent_card_routes(card), *create_jsonrpc_routes(handler, '/')])
    uvicorn.run(guard(app), host='0.0.0.0', port=9443, ssl_keyfile='/private/key.pem',
                ssl_certfile='/private/cert.pem', access_log=False, log_level='critical',
                timeout_graceful_shutdown=5, limit_concurrency=32, timeout_keep_alive=5)


if __name__ == '__main__':
    try:
        serve()
    except Exception:
        raise SystemExit('Local A2A demo startup failed; check prepared inputs.') from None
