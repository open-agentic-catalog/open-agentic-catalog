"""Owned local lifecycle qualification; explicitly selected, never an ordinary unit gate."""
import hashlib
import json
import os
from pathlib import Path
import socket
import ssl
import sys
import tempfile
import urllib.error
import urllib.request
import uuid

# Reuse the owning launcher's preparation, Compose and bounded subprocess behavior.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'monitoring'))
import launcher
import runtime_clients


def unused_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return str(listener.getsockname()[1])


def qualify():
    root = launcher.ROOT / 'output/demo/qualification'
    root.mkdir(parents=True, exist_ok=True)
    project = 'oac-demo-test-' + uuid.uuid4().hex[:12]
    report_file = root / (project + '.json')
    report = {'scope': 'owned-loopback-peer-lifecycle', 'project': project, 'success': False, 'cleanup': False}
    environment = None
    compose = launcher.compose_arguments(project, False, peers=True)
    try:
        with tempfile.TemporaryDirectory(prefix=project + '-', dir=root) as temporary:
            ap, mp = unused_port(), unused_port()
            while mp == ap:
                mp = unused_port()
            environment = launcher.runtimes.inputs(dict(os.environ, DEMO_RUNTIME_DIR=temporary,
                DEMO_A2A_PORT=ap, DEMO_MCP_PORT=mp, CATALOG_ENVIRONMENT='development', CATALOG_AUTH_MODE='development'),
                verify=True, command=launcher.command)
            private = Path(temporary)
            before = {name: hashlib.sha256((private / name).read_bytes()).hexdigest() for name in ('cert.pem', 'key.pem', 'mcp-key')}
            try:
                launcher.command(compose + ['up', '--no-deps', '-d', *launcher.runtimes.SERVICES], environment, seconds=120)
                launcher.runtimes.ready(environment, compose, launcher.command, failure=launcher.Failure)
                report['directClients'] = runtime_clients.qualify(environment)
                trust = ssl.create_default_context(cafile=private / 'cert.pem')
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), launcher.runtimes.NoRedirect(),
                    urllib.request.HTTPSHandler(context=trust))
                key = (private / 'mcp-key').read_text()
                controls = {}
                for name, port, path, body, headers, want in (
                    ('agent-bearer', ap, '/', b'{"method":"SendMessage"}', {'Authorization': 'Bearer private-negative'}, 401),
                    ('agent-oversize', ap, '/', b'x'*65537, {}, 413),
                    ('agent-unselected-method', ap, '/', b'{"method":"CancelTask"}', {}, 403),
                    ('mcp-cookie', mp, '/mcp', b'{}', {'X-MCP-Test-Key': key, 'Cookie': 'private-negative'}, 401),
                    ('mcp-bearer', mp, '/mcp', b'{}', {'X-MCP-Test-Key': key, 'Authorization': 'Bearer private-negative'}, 401),
                    ('mcp-unselected-tool', mp, '/mcp', b'{"method":"tools/call","params":{"name":"get-env"}}', {'X-MCP-Test-Key': key}, 403)):
                    request = urllib.request.Request('https://127.0.0.1:' + port + path, data=body, headers=headers)
                    try:
                        with opener.open(request, timeout=5) as response:
                            status = response.status
                    except urllib.error.HTTPError as error:
                        status = error.code
                        error.close()
                    if status != want:
                        raise ValueError('unexpected local admission outcome')
                    controls[name] = status
                try:
                    launcher.runtimes.get('https://127.0.0.1:' + ap + '/.well-known/agent-card.json', ssl.create_default_context())
                except urllib.error.URLError as error:
                    if not isinstance(error.reason, ssl.SSLCertVerificationError):
                        raise ValueError('TLS failure was not certificate verification') from error
                else:
                    raise ValueError('untrusted TLS accepted')
                report['controls'] = controls
                report['untrustedTLSRefused'] = True
                launcher.command(compose + ['stop', *launcher.runtimes.SERVICES], environment)
                for port in (ap, mp):
                    try:
                        launcher.runtimes.get('https://127.0.0.1:' + port + '/', trust)
                    except urllib.error.URLError as error:
                        if not isinstance(error.reason, ConnectionRefusedError):
                            raise ValueError('stopped peer did not establish connection refusal') from error
                    else:
                        raise ValueError('stopped peer still available')
                environment = launcher.runtimes.inputs(environment, verify=True, command=launcher.command)
                after = {name: hashlib.sha256((private / name).read_bytes()).hexdigest() for name in before}
                if before != after:
                    raise ValueError('restart rotated private identity')
                launcher.command(compose + ['up', '--no-deps', '--force-recreate', '-d', *launcher.runtimes.SERVICES], environment, seconds=120)
                launcher.runtimes.ready(environment, compose, launcher.command, failure=launcher.Failure)
                report['restartedClients'] = runtime_clients.qualify(environment)
                for service in launcher.runtimes.SERVICES:
                    raw = launcher.command(compose + ['logs', '--no-color', service], environment, capture=True)
                    if key.encode() in raw or b'private-negative' in raw:
                        raise ValueError('private value appeared in peer logs')
                report.update(identityStable=True, stopUnavailable=True, logPrivacy=True, success=True)
            finally:
                launcher.command(compose + ['down'], environment, seconds=120)
                report['cleanup'] = True
    finally:
        report_file.write_text(json.dumps(report, indent=2) + '\n')
        print('Demo lifecycle report: ' + str(report_file))
    if not report['success'] or not report['cleanup']:
        raise ValueError('incomplete demo peer qualification')


if __name__ == '__main__':
    try:
        qualify()
    except (Exception, KeyboardInterrupt):
        raise SystemExit('Owned demo peer qualification failed; inspect its outcome/cleanup report.') from None
