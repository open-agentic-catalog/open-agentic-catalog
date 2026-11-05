"""Explicit real clients for selected running demo peers; no hosted destinations."""
import hashlib
import json
import os
from pathlib import Path
import tempfile
import uuid

import runtimes


def selected_cards(environment, expected, card, mcp_card):
    """Admit discovered metadata only for these explicitly selected local peers."""
    if card != expected:
        raise ValueError('Catalog Agent Card differs from selected peer')
    remote = {'type': 'streamable-http', 'url': 'https://127.0.0.1:' + runtimes.port(environment['DEMO_MCP_PORT']) + '/mcp',
              'supportedProtocolVersions': ['2025-11-25'],
              'headers': [{'name': 'X-MCP-Test-Key',
                           'description': 'Read separately from the private local demo client input. Never use a Catalog token.',
                           'isRequired': True, 'isSecret': True}]}
    if mcp_card.get('name') != 'mcp-servers/everything' or mcp_card.get('version') != '2.0.0' or mcp_card.get('remotes') != [remote]:
        raise ValueError('Catalog MCP Card differs from selected peer')
    return card, remote['url']


def qualify(environment, *, card=None, mcp_card=None):
    a2a, mcp, _ = runtimes.tools()
    import a2a_tools
    import mcp_tools
    from runner import docker

    selected = runtimes.inputs(environment, verify=False, command=None)
    path = Path(selected['DEMO_RUNTIME_DIR'])
    state = json.loads((path / 'inputs.json').read_bytes())
    if state['a2aToolIdentity'] != a2a['identity'] or state['mcpToolIdentity'] != mcp['identity']:
        raise ValueError('prepare current demo peers first')
    expected = json.loads((path / 'agent/card.json').read_bytes())
    endpoint = 'https://127.0.0.1:' + selected['DEMO_MCP_PORT'] + '/mcp'
    if card is not None or mcp_card is not None:
        if not isinstance(card, dict) or not isinstance(mcp_card, dict):
            raise ValueError('select both discovered Cards')
        card, endpoint = selected_cards(selected, expected, card, mcp_card)
    else:
        card = expected
    report = {'scope': 'explicit-host-loopback-clients', 'a2aImage': a2a['image'], 'mcpImage': mcp['image'], 'cleanup': False}
    owned = []
    try:
        with tempfile.TemporaryDirectory(prefix='client-', dir=path) as temporary:
            client = Path(temporary)
            (client / 'ca.pem').write_bytes((path / 'cert.pem').read_bytes())
            (client / 'card.json').write_text(json.dumps(card))
            key = (path / 'mcp-key').read_text()
            if len(key) != 64 or not all(c in '0123456789abcdef' for c in key):
                raise ValueError('invalid private client input')
            config = {'mcpServers': {'demo': {'type': 'http', 'url': endpoint,
                      'headers': {'X-MCP-Test-Key': key}, 'protocolEra': 'legacy', 'roots': [],
                      'connectionTimeout': 10000, 'requestTimeout': 10000}}}
            for name, data in (('mcp.json', config), ('denied.json', {'mcpServers': {'demo': dict(config['mcpServers']['demo'], headers={})}})):
                runtimes.write(client / name, json.dumps(data).encode(), 0o600)
            def execute(kind, arguments, mounts, env=()):
                name = 'oac-demo-client-' + uuid.uuid4().hex[:12]
                owned.append(name)
                module, image = (a2a_tools, a2a['image']) if kind == 'a2a' else (mcp_tools, mcp['image'])
                args = ['--network=host']
                for origin, target in mounts:
                    args += ['--mount', f'type=bind,src={origin},dst={target},readonly']
                for value in env:
                    args += ['--env', value]
                module.create(name, *args, image, *arguments)
                result = docker('start', '--attach', name, check=False, timeout=35, limit=2*1024*1024)
                if key in result.stdout + result.stderr:
                    raise ValueError('client credential exposure')
                return result
            result = execute('a2a', ['/client/agent_client.py'], [
                (runtimes.ROOT / 'test/demo/agent_client.py', '/client/agent_client.py'),
                (client / 'ca.pem', '/client/ca.pem'), (client / 'card.json', '/client/card.json')])
            if result.code != 0:
                raise ValueError('real local A2A client failed')
            report['a2a'] = json.loads(result.stdout)
            report['mcp'] = {}
            for name, method, extra in (('initialize', 'initialize', []), ('tools-list', 'tools/list', []),
                                        ('echo', 'tools/call', ['--tool-name', 'echo', '--tool-arg', 'message=Catalog demo local echo']),
                                        ('missing-key', 'tools/list', [])):
                filename = 'denied.json' if name == 'missing-key' else 'mcp.json'
                result = execute('mcp', [mcp_tools.INSPECTOR, '--cli', '--config', '/client/' + filename,
                    '--server', 'demo', '--method', method, '--format', 'json', '--stored-auth-only', *extra],
                    [(client / filename, '/client/' + filename), (client / 'ca.pem', '/client/ca.pem')],
                    ('NODE_EXTRA_CA_CERTS=/client/ca.pem', 'MCP_CLIENT_CONFIG_PATH=/tmp/settings.json', 'MCP_INSPECTOR_SECRET_STORE=memory'))
                if name == 'missing-key':
                    if result.code != 3 or json.loads(result.stderr.strip().splitlines()[-1])['error']['code'] != 'auth_required':
                        raise ValueError('missing-key denial not established')
                    report['mcp'][name] = True
                    continue
                if result.code != 0:
                    raise ValueError('real local MCP client failed')
                data = json.loads(result.stdout)['result']
                if name == 'initialize':
                    if data['protocolVersion'] != '2025-11-25' or data['serverInfo'].get('name') != 'mcp-servers/everything' or data['serverInfo'].get('version') != '2.0.0':
                        raise ValueError('MCP identity mismatch')
                elif name == 'tools-list':
                    if 'echo' not in [tool['name'] for tool in data['tools']]:
                        raise ValueError('missing actual echo tool')
                elif data.get('isError', False) or data['content'] != [{'type': 'text', 'text': 'Echo: Catalog demo local echo'}]:
                    raise ValueError('MCP echo mismatch')
                report['mcp'][name] = True
            report['trustSHA256'] = hashlib.sha256((client / 'ca.pem').read_bytes()).hexdigest()
    finally:
        # Remove only names created here; never stop selected persistent peers.
        a2a_tools.remove(owned)
        report['cleanup'] = True
    print(json.dumps(report, indent=2))
    return report


if __name__ == '__main__':
    try:
        qualify(dict(os.environ))
    except (Exception, KeyboardInterrupt):
        raise SystemExit('Demo local client qualification failed; check peer readiness, installed tools and private inputs.') from None
