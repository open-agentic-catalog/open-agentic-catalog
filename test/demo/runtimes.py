"""Fixed local peers for the existing Compose launcher; never a YAML runtime manager."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import ssl
import stat
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ('demo-agent', 'demo-mcp', 'demo-everything')


def tools():
    # Reuse exact reviewed acquisition receipts and fail closed; no implicit install.
    sys.path.insert(0, str(ROOT / 'test/interoperability'))
    import a2a_tools
    import mcp_tools
    from runner import local_daemon
    return a2a_tools.installed(), mcp_tools.installed(), local_daemon()


def port(value):
    if not isinstance(value, str) or not re.fullmatch(r'[0-9]{4,5}', value) or not 1024 <= int(value) <= 65535 or str(int(value)) != value:
        raise ValueError('invalid demo runtime port')
    return value


def write(path, raw, mode):
    if path.is_symlink() or path.exists() and (not path.is_file() or path.stat().st_uid != os.getuid()):
        raise ValueError('unsafe demo runtime input')
    descriptor, temporary = tempfile.mkstemp(dir=path.parent, prefix='.prepare-')
    try:
        with os.fdopen(descriptor, 'wb') as output:
            os.fchmod(output.fileno(), mode)
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def directory(path, create):
    path = Path(path).absolute()
    for parent in [*reversed(path.parents), path]:
        if parent.is_symlink():
            raise ValueError('unsafe demo runtime directory')
    if create:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.exists() and (not path.is_dir() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077):
        raise ValueError('unsafe demo runtime directory')
    return path


def inputs(environment, verify, command):
    result = dict(environment)
    if os.getuid() == 0 or result.get('CATALOG_ENVIRONMENT', 'development') != 'development' or result.get('CATALOG_AUTH_MODE', 'development') != 'development':
        raise ValueError('demo runtimes require non-root development')
    path = directory(result.get('DEMO_RUNTIME_DIR', str(ROOT / 'output/demo/runtimes')), create=False)
    state = {}
    state_file = path / 'inputs.json'
    if state_file.is_symlink():
        raise ValueError('unsafe demo runtime state')
    if state_file.exists():
        if state_file.is_symlink() or not state_file.is_file() or state_file.stat().st_uid != os.getuid() or state_file.stat().st_mode & 0o022 or state_file.stat().st_size > 16384:
            raise ValueError('unsafe demo runtime state')
        state = json.loads(state_file.read_bytes())
        if not isinstance(state, dict):
            raise ValueError('invalid demo runtime state')
    ap = port(result.get('DEMO_A2A_PORT', state.get('a2aPort', '19443')))
    mp = port(result.get('DEMO_MCP_PORT', state.get('mcpPort', '19444')))
    if ap == mp:
        raise ValueError('conflicting demo runtime ports')
    if verify:
        a2a, mcp, arch = tools()
        directory(path, create=True)
        lock_path = path / '.prepare.lock'
        descriptor = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        with os.fdopen(descriptor, 'r+') as lock:
            info = os.fstat(lock.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o022:
                raise ValueError('unsafe demo runtime lock')
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            env = {key: value for key, value in result.items() if key in ('PATH', 'HOME', 'GOCACHE', 'GOMODCACHE')}
            env.update(CGO_ENABLED='0', GOOS='linux', GOARCH=arch, GOFLAGS='-mod=readonly')
            # Build before replacing the old executable: a failure leaves existing peer inputs usable.
            binary = path / 'mcpfixture.next'
            if binary.is_symlink() or binary.exists() and (not binary.is_file() or binary.stat().st_uid != os.getuid()):
                raise ValueError('unsafe demo runtime binary')
            command([result.get('GO', 'go'), 'build', '-trimpath', '-o', str(binary), './test/interoperability/mcpfixture'], env, seconds=120)
            command([result.get('GO', 'go'), 'run', './test/demo', '--prepare-runtimes', '--directory', str(path)], result, seconds=120)
            for sub in ('agent', 'mcp'):
                directory(path / sub, create=True)
            cert, key = (path / 'cert.pem').read_bytes(), (path / 'key.pem').read_bytes()
            runtime_key = (path / 'mcp-key').read_bytes()
            for sub, names in (('agent', ('cert.pem', 'key.pem')), ('mcp', ('server.pem', 'server.key'))):
                write(path / sub / names[0], cert, 0o644)
                write(path / sub / names[1], key, 0o600)
            write(path / 'mcp/key', runtime_key, 0o600)
            card = json.loads((ROOT / 'samples/cards/hello-world.json').read_bytes())
            card['supportedInterfaces'][0]['url'] = 'https://127.0.0.1:' + ap + '/'
            write(path / 'agent/card.json', json.dumps(card).encode(), 0o644)
            os.replace(binary, path / 'mcpfixture')
            state = {'a2aImage': a2a['image'], 'mcpImage': mcp['image'], 'a2aPort': ap, 'mcpPort': mp,
                     'a2aToolIdentity': a2a['identity'], 'mcpToolIdentity': mcp['identity'],
                     'wrapperSHA256': hashlib.sha256((path / 'mcpfixture').read_bytes()).hexdigest()}
            write(state_file, (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    for field in ('a2aImage', 'mcpImage'):
        value = state.get(field, 'sha256:' + '0'*64)
        if not isinstance(value, str) or not re.fullmatch('sha256:[0-9a-f]{64}', value):
            raise ValueError('invalid demo runtime image')
    result.update(DEMO_RUNTIME_DIR=str(path), DEMO_RUNTIME_UID=str(os.getuid()), DEMO_RUNTIME_GID=str(os.getgid()),
                  DEMO_A2A_PORT=ap, DEMO_MCP_PORT=mp, DEMO_A2A_IMAGE=state.get('a2aImage', 'sha256:' + '0'*64),
                  DEMO_MCP_IMAGE=state.get('mcpImage', 'sha256:' + '0'*64))
    return result


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, _req, _fp, _code, _msg, _headers, _newurl):
        return None


def get(url, trust):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPSHandler(context=trust))
    try:
        with opener.open(url, timeout=2) as response:
            raw = response.read(65537)
            if len(raw) > 65536:
                raise ValueError('demo peer response limit')
            return response.status, raw
    except urllib.error.HTTPError as error:
        try:
            return error.code, b''
        finally:
            error.close()


def ready(environment, compose, command, failure=RuntimeError):
    path = Path(environment['DEMO_RUNTIME_DIR'])
    trust = ssl.create_default_context(cafile=path / 'cert.pem')
    expected = json.loads((path / 'agent/card.json').read_bytes())
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        try:
            status, raw = get('https://127.0.0.1:' + environment['DEMO_A2A_PORT'] + '/.well-known/agent-card.json', trust)
            mcp_status, _ = get('https://127.0.0.1:' + environment['DEMO_MCP_PORT'] + '/mcp', trust)
            if status == 200 and json.loads(raw) == expected and mcp_status == 401:
                command(compose + ['exec', '-T', 'demo-mcp', '/deployment/mcpfixture', 'probe'], environment, seconds=5, capture=True)
                return
        except (OSError, ValueError, RuntimeError, failure):
            pass
        time.sleep(0.2)
    raise ValueError('demo peers did not become ready')
