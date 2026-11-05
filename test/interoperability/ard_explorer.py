"""Explicit acquisition and confined local qualification of unchanged ARD Explorer."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid

from common import run
from runner import ROOT, HERE, extract_source, fingerprint, cached_go, build_binaries, CATALOG_INPUTS

INPUTS = HERE / 'ard-explorer'
OUTPUT = ROOT / 'output/interoperability/ard-explorer'
TOOLS = OUTPUT / 'tools'
PIN = json.loads((INPUTS / 'inputs.json').read_text())
NODE_ROOT = ROOT / '.tools/ci' / ('node-v' + PIN['node'] + '-linux-x64')
BROWSER_MODULES = ROOT / 'catalog-ui/node_modules'
HARNESS_INPUTS = (INPUTS, HERE / 'ard_explorer.py', HERE / 'common.py', HERE / 'runner.py',
                  ROOT / 'catalog-ui/scripts/ard-explorer-startup.mjs',
                  ROOT / 'catalog-ui/scripts/ard-explorer-journeys.mjs', ROOT / 'Makefile')


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tree_digest(directory):
    value = hashlib.sha256()
    for path in sorted(directory.rglob('*')):
        if path.is_file():
            value.update(str(path.relative_to(directory)).encode() + b'\0' + bytes.fromhex(sha(path)))
    return value.hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')
    path.chmod(0o600)


def environment(home):
    """No user npm configuration, tokens, endpoint history or injected proxy."""
    return {'PATH': str(NODE_ROOT / 'bin') + ':/usr/bin:/bin', 'HOME': str(home),
            'LANG': 'C.UTF-8', 'npm_config_userconfig': '/dev/null',
            'npm_config_globalconfig': str(home / 'global.npmrc'), 'npm_config_audit': 'false',
            'npm_config_fund': 'false', 'npm_config_ignore_scripts': 'true'}


def prerequisites():
    if sys.flags.optimize:
        raise RuntimeError('Explorer qualification requires Python assertions')
    if platform.system() != 'Linux' or platform.machine() != 'x86_64' or os.getuid() == 0:
        raise RuntimeError('Explorer qualification requires a non-root Linux AMD64 host')
    for command in ('bwrap', 'systemd-run', 'systemctl'):
        if not shutil.which(command):
            raise RuntimeError('missing Explorer confinement prerequisite: ' + command)
    if run([str(NODE_ROOT / 'bin/node'), '--version']).stdout.strip() != 'v' + PIN['node']:
        raise RuntimeError('install the pinned repository Node toolchain first')
    if run([str(NODE_ROOT / 'bin/node'), str(NODE_ROOT / 'lib/node_modules/npm/bin/npm-cli.js'), '--version']).stdout.strip() != PIN['npm']:
        raise RuntimeError('npm toolchain differs from the reviewed pin')


def validate_scan(report, lock):
    """A scanner error or omitted development package is not a clean scan."""
    if report.get('SchemaVersion') != 2 or len(report.get('Results', [])) != 1:
        raise ValueError('incomplete Explorer advisory scan')
    result = report['Results'][0]
    expected = {(p.rsplit('node_modules/', 1)[1], v['version']) for p, v in lock['packages'].items() if p}
    actual = {(p['Name'], p['Version']) for p in result.get('Packages', [])}
    if result.get('Type') != 'npm' or result.get('Target') != 'package-lock.json' or not expected <= actual:
        raise ValueError('Explorer scan omits locked packages')
    findings = result.get('Vulnerabilities', [])
    # The unchanged build uses the esbuild CLI, never its vulnerable serve API.
    allowed = ('GHSA-67mh-4wv8-2f99', 'esbuild', '0.24.2')
    if any((f['VulnerabilityID'], f['PkgName'], f['InstalledVersion']) != allowed for f in findings):
        raise ValueError('unreviewed Explorer advisory; review before selecting this closure')
    return findings


def browser_identity():
    """Reuse installed UI tools; never acquire a browser during execution."""
    package = json.loads((BROWSER_MODULES / 'playwright/package.json').read_text())
    browsers = json.loads((BROWSER_MODULES / 'playwright-core/browsers.json').read_text())
    selected = next(b for b in browsers['browsers'] if b['name'] == 'chromium')
    if package['version'] != PIN['playwright'] or selected['revision'] != PIN['chromiumRevision']:
        raise RuntimeError('Explorer browser tools differ from reviewed UI pins')
    path = run([str(NODE_ROOT / 'bin/node'), '--input-type=module', '-e',
                'import {chromium} from ' + json.dumps(str(BROWSER_MODULES / 'playwright/index.mjs')) + ';console.log(chromium.executablePath())']).stdout.strip()
    executable = Path(path)
    return {'version': selected['browserVersion'], 'revision': selected['revision'],
            'executable': str(executable), 'sha256': sha(executable), 'playwright': package['version'],
            'distributionDigest': tree_digest(executable.parents[1]),
            'moduleDigest': fingerprint([BROWSER_MODULES / p for p in ('playwright', 'playwright-core', '@playwright/test')])}


def install():
    """Download only explicit source/lock inputs, with all npm lifecycle scripts disabled."""
    prerequisites()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    if TOOLS.exists():
        raise RuntimeError('Explorer tools already exist; retain or remove only this tool directory before reinstalling')
    TOOLS.mkdir(mode=0o700)
    source = TOOLS / 'source'
    source.mkdir()
    raw = run(['gh', 'api', 'repos/' + PIN['repository'] + '/tarball/' + PIN['revision']], binary=True).stdout
    extract_source(raw, source, PIN['sourceDigest'])
    source_files = sorted(str(p.relative_to(source)) for p in source.rglob('*') if p.is_file())
    if sha(source / 'package-lock.json') != PIN['lockDigest'] or sha(source / 'LICENSE') != PIN['licenseDigest']:
        raise RuntimeError('Explorer lock or license differs from reviewed source')
    lock = json.loads((source / 'package-lock.json').read_text())
    licenses = sorted({p.get('license') for p in lock['packages'].values()})
    if not set(licenses) <= {'MIT', 'Apache-2.0', '0BSD'}:
        raise RuntimeError('unreviewed Explorer license')
    home = TOOLS / 'home'
    home.mkdir()
    (home / 'global.npmrc').write_text('')
    env = environment(home)
    for name, args in [('acquisition', ['ci', '--ignore-scripts', '--no-audit', '--no-fund']), ('build', ['run', 'build'])]:
        result = run([str(NODE_ROOT / 'bin/npm'), '--prefix', str(source), *args], env=env, timeout=180, check=False)
        (TOOLS / (name + '.log')).write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError('Explorer ' + name + ' failed; inspect private installation log')
    scanner = ROOT / '.tools/ci/bin/trivy'
    version = json.loads(run([str(scanner), '--version', '--format=json']).stdout)
    if version['Version'] != json.loads((ROOT / 'test/packaging/trivy.json').read_text())['version']:
        raise RuntimeError('install the pinned scanner first')
    # Download advisory data without submitting dependency names or source bytes.
    run([str(scanner), 'image', '--download-db-only', '--disable-telemetry'], timeout=180)
    run([str(scanner), 'fs', '--scanners', 'vuln', '--include-dev-deps', '--list-all-pkgs', '--skip-db-update',
         '--skip-java-db-update', '--offline-scan', '--disable-telemetry', '--format', 'json',
         '--output', str(TOOLS / 'advisories.json'), str(source)], timeout=90)
    findings = validate_scan(json.loads((TOOLS / 'advisories.json').read_text()), lock)
    inventory = []
    for path in sorted((source / 'node_modules').rglob('package.json')):
        value = json.loads(path.read_text())
        if value.get('name') and value.get('version'):
            inventory.append({'path': str(path.relative_to(source)), 'name': value['name'], 'version': value['version'],
                              'license': value.get('license'), 'scripts': {k: v for k, v in value.get('scripts', {}).items()
                              if k in ('preinstall', 'install', 'postinstall', 'prepare')}})
    save(TOOLS / 'inventory.json', inventory)
    notices = {str(p.relative_to(source)): sha(p) for p in source.rglob('*') if p.is_file()
               and p.name.lower().startswith(('license', 'notice', 'copyright'))}
    save(TOOLS / 'notices.json', notices)
    stamp = {'revision': PIN['revision'], 'sourceFiles': source_files, 'licenses': licenses, 'findings': findings,
             'browser': browser_identity(), 'nodeDigest': sha(NODE_ROOT / 'bin/node'),
             'scanner': version, 'inputs': fingerprint([INPUTS / 'inputs.json']),
             'toolDigest': fingerprint([source]), 'installedUTC': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
    save(TOOLS / 'installation.json', stamp)
    print('Explorer tools installed locally. Run make test-interoperability-ard-explorer separately.')


def installed():
    prerequisites()
    stamp = json.loads((TOOLS / 'installation.json').read_text())
    if (stamp['revision'] != PIN['revision'] or stamp['inputs'] != fingerprint([INPUTS / 'inputs.json'])
            or stamp['toolDigest'] != fingerprint([TOOLS / 'source'])
            or stamp['nodeDigest'] != sha(NODE_ROOT / 'bin/node') or stamp['browser'] != browser_identity()):
        raise RuntimeError('Explorer installation changed; review and reinstall explicitly')
    return stamp


def sandbox(directory, browser):
    """Expose only tool files and one disk-backed private state directory."""
    args = ['bwrap', '--unshare-user', '--unshare-ipc', '--unshare-pid', '--unshare-net', '--unshare-uts',
            '--die-with-parent', '--new-session', '--clearenv',
            '--ro-bind', '/usr', '/usr', '--ro-bind', '/lib', '/lib', '--ro-bind', '/lib64', '/lib64',
            '--symlink', 'usr/bin', '/bin', '--proc', '/proc', '--dev', '/dev',
            '--ro-bind', '/sys/fs/cgroup', '/sys/fs/cgroup', '--ro-bind', '/etc/fonts', '/etc/fonts',
            '--ro-bind', '/etc/ld.so.cache', '/etc/ld.so.cache',
            '--ro-bind', str(NODE_ROOT), '/node', '--ro-bind', str(TOOLS / 'source'), '/explorer',
            '--ro-bind', str(BROWSER_MODULES), '/browser/node_modules',
            '--ro-bind', str(Path(browser['executable']).parents[1]), '/chromium',
            '--ro-bind', str(INPUTS / 'confined.py'), '/harness/confined.py', '--bind', str(directory), '/state',
            '--ro-bind', str(INPUTS / 'fixture.py'), '/harness/fixture.py',
            '--ro-bind', str(ROOT / 'catalog-ui/scripts/ard-explorer-startup.mjs'), '/browser/scripts/ard-explorer-startup.mjs',
            '--ro-bind', str(ROOT / 'catalog-ui/scripts/ard-explorer-journeys.mjs'), '/browser/scripts/ard-explorer-journeys.mjs',
            '--ro-bind', str(INPUTS / 'hosts'), '/etc/hosts',
            '--bind', str(directory / 'tmp'), '/tmp', '--chdir', '/state',
            '--setenv', 'PATH', '/node/bin:/usr/bin:/bin', '--setenv', 'HOME', '/tmp/home',
            '--setenv', 'LANG', 'C.UTF-8', '--setenv', 'PYTHONDONTWRITEBYTECODE', '1',
            '/usr/bin/python3', '-B', '/harness/confined.py']
    return args


def scope_command(unit, command):
    bounds = PIN['bounds']
    return ['systemd-run', '--user', '--wait', '--pipe', '--collect', '--quiet', '--unit=' + unit,
            '-p', 'RuntimeMaxSec=' + str(bounds['seconds']), '-p', 'MemoryMax=' + str(bounds['memoryBytes']),
            '-p', 'MemorySwapMax=0', '-p', 'CPUQuota=' + bounds['cpuQuota'], '-p', 'TasksMax=' + str(bounds['tasks']),
            '-p', 'KillMode=control-group', *command]


def validate_startup(value, browser):
    """Require the complete native controls, not just an overall success flag."""
    if not isinstance(value, dict) or set(value) != {'passed', 'network', 'externalConnectErrno', 'memoryMax', 'cpuCores',
                                                   'tasksMax', 'swapMax', 'browser', 'origin'}:
        raise ValueError('incomplete Explorer startup evidence')
    expected = {'passed': True, 'network': 'loopback-only', 'externalConnectErrno': 101,
                'memoryMax': PIN['bounds']['memoryBytes'], 'cpuCores': 2, 'tasksMax': PIN['bounds']['tasks'], 'swapMax': 0}
    if any(type(value[k]) is not type(v) or value[k] != v for k, v in expected.items()):
        raise ValueError('Explorer confinement evidence differs')
    native = {'passed': True, 'proxyOperations': 2, 'externalProxyRefused': True, 'externalBrowserRefused': True,
              'browserVersion': browser['version'], 'errors': [], 'refused': []}
    if value['browser'] != native or type(value['browser'].get('proxyOperations')) is not int:
        raise ValueError('incomplete Explorer browser evidence')
    from urllib.parse import urlsplit
    origin = urlsplit(value['origin'])
    if origin.scheme != 'http' or origin.hostname != '127.0.0.1' or not origin.port or origin.path != '/' or origin.query or origin.fragment or origin.username:
        raise ValueError('unowned Explorer browser origin')


CASE_DISPOSITIONS = dict.fromkeys(('manifest-connect', 'registry-connect', 'search-pagination', 'changed-filter-token',
    'filter-match', 'filter-empty', 'explore-counts', 'list-first-page', 'sparse-unfamiliar', 'malformed-envelope',
    'http-400', 'http-401', 'http-503', 'target-unavailable'), 'supported')
CASE_DISPOSITIONS.update(dict.fromkeys(('list-continuation', 'private-authentication', 'query-context', 'full-referral-connect'), 'client-limitation'))


def validate_browsing(value):
    """A reproduced client limit is not a supported capability or an empty pass."""
    required = {'outcomes', 'proxyOperations', 'exchanges', 'catalogCounters', 'snapshotStable', 'originalETag', 'resources', 'fixtureObservations'}
    if not isinstance(value, dict) or set(value) != required or value['snapshotStable'] is not True:
        raise ValueError('incomplete Explorer browsing evidence')
    cases = value['outcomes']
    if not isinstance(cases, list) or len(cases) != len(CASE_DISPOSITIONS) or {c['name'] for c in cases} != set(CASE_DISPOSITIONS):
        raise ValueError('incomplete Explorer case inventory')
    for case in cases:
        if case['status'] != 'pass' or case['disposition'] != CASE_DISPOSITIONS[case['name']] or not isinstance(case['evidence'], dict):
            raise ValueError('incorrect Explorer case verdict')
    by_name = {c['name']: c['evidence'] for c in cases}
    if type(value['proxyOperations']) is not int or not 18 <= value['proxyOperations'] <= PIN['bounds']['proxyOperations'] or len(value['exchanges']) != value['proxyOperations']:
        raise ValueError('Explorer proxy operation accounting differs')
    resources = value['resources']
    if len(resources) != 3 or {r['kind'] for r in resources} != {'agent', 'mcp_server', 'skill'}:
        raise ValueError('Explorer fixture authority is incomplete')
    identifiers = {r['identifier'] for r in resources}
    pages = by_name['search-pagination']['pages']
    if (len(pages) != PIN['bounds']['searchPages'] or len({p['identifier'] for p in pages}) != 3
            or {p['identifier'] for p in pages} != identifiers or pages[-1]['token'] is not None
            or any(not isinstance(p['token'], str) or not p['token'] for p in pages[:-1])
            or len({p['token'] for p in pages[:-1]}) != 2):
        raise ValueError('Explorer page walk is incomplete')
    if any(type(p['score']) is not int or not 0 <= p['score'] <= 100 for p in pages):
        raise ValueError('Explorer page score differs')
    expected = {'manifest-connect': {'requests': 4}, 'registry-connect': {'requests': 3},
                'changed-filter-token': {'status': 400}, 'filter-empty': {'results': 0},
                'filter-match': {'identifier': next(r['identifier'] for r in resources if r['kind'] == 'mcp_server')},
                'list-continuation': {'receivedToken': True, 'nextControl': False},
                'private-authentication': {'statuses': [401, 401, 401], 'authorization': False},
                'sparse-unfamiliar': {'results': 3}, 'malformed-envelope': {'status': 200, 'rejectedEnvelope': True},
                'query-context': {'status': 400, 'queryContext': False},
                'target-unavailable': {'outerStatus': 200, 'transportFailed': True}}
    expected.update({'http-' + str(status): {'outerStatus': 200, 'status': status} for status in (400, 401, 503)})
    if any(by_name[name] != evidence for name, evidence in expected.items()):
        raise ValueError('Explorer control evidence differs')
    if by_name['full-referral-connect']['statuses'] != [404, 404, 404] or not by_name['full-referral-connect']['searchURL'].endswith('/referral-target/search/search'):
        raise ValueError('Explorer referral limitation differs')
    buckets = by_name['explore-counts']['buckets']
    if len(buckets) != 3 or any(b['count'] != 1 for b in buckets) or len({b['value'] for b in buckets}) != 3:
        raise ValueError('Explorer facet evidence differs')
    if not value['catalogCounters'] or not value['fixtureObservations'] or not value['originalETag']:
        raise ValueError('Explorer independent observations are missing')


def require_user_manager():
    """Observe an existing user manager. Never start services or enable lingering."""
    result = run(['systemctl', '--user', 'show', '--property=Version', '--value'], check=False, timeout=10)
    if result.code != 0 or not result.stdout.strip():
        raise RuntimeError('Explorer requires an available systemd user manager; no user manager was started')


def stop_scope(unit):
    try:
        require_user_manager()
    except RuntimeError as error:
        raise RuntimeError('Explorer scope cleanup could not be verified: user manager unavailable') from error
    run(['systemctl', '--user', 'stop', unit], check=False, timeout=15)
    state = run(['systemctl', '--user', 'is-active', unit], check=False, timeout=10)
    properties = run(['systemctl', '--user', 'show', unit, '--property=LoadState', '--property=ControlGroup'], check=False, timeout=10)
    if state.code not in (0, 3, 4) or properties.code != 0 or not state.stdout.strip():
        raise RuntimeError('Explorer scope cleanup could not be verified: user-manager query failed')
    fields = dict(line.split('=', 1) for line in properties.stdout.splitlines() if '=' in line)
    if set(fields) != {'LoadState', 'ControlGroup'} or not fields['LoadState']:
        raise RuntimeError('Explorer scope cleanup could not be verified: incomplete unit properties')
    if state.stdout.strip() not in ('inactive', 'failed', 'unknown'):
        raise RuntimeError('Explorer scope remains active: ' + unit)
    group = fields['ControlGroup']
    if group and (Path('/sys/fs/cgroup') / group.lstrip('/') / 'cgroup.procs').exists():
        if (Path('/sys/fs/cgroup') / group.lstrip('/') / 'cgroup.procs').read_text().strip():
            raise RuntimeError('Explorer child remains in its cgroup: ' + unit)


def cleanup_controls(directory, browser):
    """Exercise failure, supervisor timeout and interruption against actual owned services."""
    outcomes = []
    for case in ('failure', 'timeout', 'interruption'):
        unit = 'oac-ard-control-' + uuid.uuid4().hex[:12] + '.service'
        code = ('raise SystemExit(17)' if case == 'failure' else
                'import subprocess,time,pathlib; subprocess.Popen(["/usr/bin/sleep","60"]); '
                'pathlib.Path("/state/control-ready").write_text("ready"); time.sleep(60)')
        command = scope_command(unit, [*sandbox(directory, browser)[:-1], '-c', code])
        ready = directory / 'control-ready'
        try:
            if case == 'interruption':
                with subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as child:
                    try:
                        deadline = time.monotonic() + 10
                        while not ready.exists() and time.monotonic() < deadline and child.poll() is None:
                            time.sleep(0.05)
                        if not ready.exists():
                            raise RuntimeError('interruption control never started')
                        child.send_signal(signal.SIGINT)
                        child.wait(timeout=5)
                    finally:
                        if child.poll() is None:
                            child.kill()
                            child.wait(timeout=5)
            elif case == 'timeout':
                try:
                    run(command, timeout=2)
                except RuntimeError as error:
                    if str(error) != 'subprocess deadline exceeded' or not ready.exists():
                        raise
                else:
                    raise RuntimeError('timeout control did not time out')
            else:
                result = run(command, check=False, timeout=10)
                (directory / 'failure-control.log').write_text(result.stdout + result.stderr)
                if result.code != 17:
                    raise RuntimeError('failure control did not preserve child exit')
        finally:
            stop_scope(unit)
            ready.unlink(missing_ok=True)
        outcomes.append(case)
    return outcomes


def cleanup_run(directory, unit, report):
    """A passing scenario still fails if process or temporary-state cleanup fails."""
    try:
        stop_scope(unit)
        for name in ('catalogd', 'catalogctl', 'config.json'):
            (directory / name).unlink(missing_ok=True)
        shutil.rmtree(directory / 'tmp')
        report['cleanup'] = 'passed'
    except BaseException:
        report.update(status='failed', cleanup='failed')
        raise
    finally:
        save(directory / 'result.json', report)


def check(browsing=False):
    stamp = installed()
    require_user_manager()
    before = fingerprint(CATALOG_INPUTS)
    harness = fingerprint(HARNESS_INPUTS)
    directory = Path(tempfile.mkdtemp(prefix='browse-' if browsing else 'startup-', dir=OUTPUT))
    (directory / 'tmp/home').mkdir(parents=True)
    unit = 'oac-ard-explorer-' + uuid.uuid4().hex[:12] + '.service'
    report = {'stage': 'browsing' if browsing else 'startup-only', 'status': 'failed', 'cleanup': 'pending', 'unit': unit,
              'source': before, 'harness': harness, 'client': stamp, 'bounds': PIN['bounds']}
    def interrupted(_number, _frame):
        raise KeyboardInterrupt
    previous = {s: signal.signal(s, interrupted) for s in (signal.SIGINT, signal.SIGTERM)}
    try:
        go = cached_go()
        build_binaries(directory, go, 'amd64')
        report['build'] = {'go': run([go, 'version']).stdout.strip(),
                           'catalogd': sha(directory / 'catalogd'), 'catalogctl': sha(directory / 'catalogctl')}
        config = json.loads((ROOT / 'config/local.json').read_text())
        if browsing:
            public = config['discovery']['hosts'][0]
            config['discovery']['hosts'].append(dict(public, host='localhost', mode='authenticated'))
        save(directory / 'config.json', config)
        report['cleanupControls'] = cleanup_controls(directory, stamp['browser'])
        bounds = PIN['bounds']
        command = sandbox(directory, stamp['browser'])
        if browsing:
            command.append('browse')
        result = run(scope_command(unit, command), timeout=bounds['seconds'] + 20, check=False)
        (directory / 'native.log').write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError('confined Explorer qualification failed; inspect ' + str(directory / 'native.log'))
        report['startup'] = json.loads((directory / 'startup.json').read_text())
        validate_startup(report['startup'], stamp['browser'])
        if browsing:
            report['browsing'] = json.loads((directory / 'browsing.json').read_text())
            validate_browsing(report['browsing'])
        if before != fingerprint(CATALOG_INPUTS) or harness != fingerprint(HARNESS_INPUTS) or stamp != installed():
            raise RuntimeError('incomplete or changed Explorer qualification')
        report['status'] = 'passed'
    finally:
        try:
            cleanup_run(directory, unit, report)
        finally:
            for s, handler in previous.items():
                signal.signal(s, handler)
    print(('Explorer browsing and confinement passed. Report: ' if browsing else
           'Explorer startup and confinement passed; browsing is not selected. Report: ') + str(directory / 'result.json'))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('install', 'check', 'browse'))
    arguments = parser.parse_args()
    try:
        if arguments.action == 'install':
            install()
        else:
            check(arguments.action == 'browse')
    except (OSError, RuntimeError, ValueError, KeyError, KeyboardInterrupt) as error:
        print('Explorer: ' + str(error), file=sys.stderr)
        raise SystemExit(1) from None
