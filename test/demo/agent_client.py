"""Actual SDK consumer for one explicitly selected host-loopback Hello World peer."""
import asyncio
import json
from pathlib import Path
import ssl
import sys
from urllib.parse import urlsplit


async def client():
    import httpx
    from a2a.client import ClientConfig, create_client
    from a2a.types import AgentCard, GetTaskRequest, Message, Part, Role, SendMessageRequest
    from google.protobuf.json_format import MessageToDict, ParseDict

    card = json.loads(Path('/client/card.json').read_bytes())
    endpoint = card['supportedInterfaces'][0]['url']
    target = urlsplit(endpoint)
    assert target.scheme == 'https' and target.hostname == '127.0.0.1' and target.port is not None
    assert target.netloc == '127.0.0.1:' + str(target.port) and target.path == '/'
    assert not target.query and not target.fragment and 1024 <= target.port <= 65535
    assert len(card['supportedInterfaces']) == 1
    assert card['supportedInterfaces'][0]['protocolBinding'] == 'JSONRPC'
    assert card['supportedInterfaces'][0]['protocolVersion'] == '1.0'
    marker = 'Catalog persistent local peer qualification café'
    trust = ssl.create_default_context(cafile='/client/ca.pem')
    async with httpx.AsyncClient(verify=trust, trust_env=False, follow_redirects=False,
                                 timeout=5, headers={'A2A-Version': '1.0'}) as http:
        response = await http.get(endpoint + '.well-known/agent-card.json')
        response.raise_for_status()
        assert response.json() == card and len(response.content) <= 65536
        sdk = await create_client(ParseDict(card, AgentCard()), client_config=ClientConfig(
            httpx_client=http, supported_protocol_bindings=['JSONRPC'], streaming=False, polling=False,
            accepted_output_modes=['text/plain']))
        async with sdk:
            message = Message(message_id='demo-peer-send', context_id='demo-peer-context', role=Role.ROLE_USER,
                              parts=[Part(text=marker, media_type='text/plain')])
            responses = [MessageToDict(event) async for event in sdk.send_message(SendMessageRequest(message=message))]
            assert len(responses) == 1 and set(responses[0]) == {'task'}
            task = responses[0]['task']
            assert task['status']['state'] == 'TASK_STATE_COMPLETED'
            assert task['artifacts'][0]['parts'] == [{'text': 'Hello, World! I have received your request (' + marker + ')', 'mediaType': 'text/plain'}]
            readback = MessageToDict(await sdk.get_task(GetTaskRequest(id=task['id'])))
            assert readback == task
        print(json.dumps({'cardMatched': True, 'completedTask': True, 'exactArtifact': True, 'readbackMatched': True}))


if __name__ == '__main__':
    try:
        asyncio.run(asyncio.wait_for(client(), timeout=20))
    except Exception:
        print('Local A2A client qualification failed.', file=sys.stderr)
        sys.exit(1)
